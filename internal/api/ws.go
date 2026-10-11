package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/linuxsuren/open-fitpose-server/internal/analyze"
	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// WS 会话参数。
const (
	wsWriteWait  = 10 * time.Second // 单次写超时
	wsPongWait   = 75 * time.Second // 读空闲超时
	wsPingPeriod = 30 * time.Second // 传输层心跳周期
	wsDetectWait = 5 * time.Second  // 单帧推理超时
)

// clientMessage 客户端上行消息（type 分发）。
type clientMessage struct {
	Type   string `json:"type"`             // hello | frame | ping
	Seq    int64  `json:"seq,omitempty"`    // frame：客户端自增序号，原样回传
	Format string `json:"format,omitempty"` // frame：图像编码，当前支持 jpeg
	Data   string `json:"data,omitempty"`   // frame：base64 图像
	TS     int64  `json:"ts,omitempty"`     // ping：客户端时间戳
}

// serverMessage 服务端下行消息（type 分发）。
type serverMessage struct {
	Type          string            `json:"type"`
	Protocol      int               `json:"protocol,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	KeypointNames []string          `json:"keypoint_names,omitempty"`
	Seq           int64             `json:"seq,omitempty"`
	Keypoints     []pose.Keypoint   `json:"keypoints"`
	Analysis      *analyze.Analysis `json:"analysis,omitempty"`
	Tips          []string          `json:"tips,omitempty"`
	ServerMS      int64             `json:"server_ms,omitempty"`
	Code          string            `json:"code,omitempty"`
	Detail        string            `json:"detail,omitempty"`
	TS            int64             `json:"ts,omitempty"`
}

// upgrader WS 升级器。局域网服务，默认放行跨源；需要更强约束时配置 API Key。
var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// handleWS 处理 WebSocket 实时识别会话。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Info("ws upgrade failed", "remote", r.RemoteAddr, "err", err)
		return
	}
	sess := &wsSession{
		srv:      s,
		conn:     conn,
		analyzer: analyze.NewAnalyzer(),
		frames:   make(chan clientMessage, 1),
		closed:   make(chan struct{}),
	}
	s.log.Info("ws connected", "remote", r.RemoteAddr, "provider", s.opts.Provider.Name())
	sess.run()
	s.log.Info("ws disconnected", "remote", r.RemoteAddr)
}

// wsSession 单个 WS 会话：读循环 + 处理循环 + 心跳。
type wsSession struct {
	srv       *Server
	conn      *websocket.Conn
	analyzer  *analyze.Analyzer
	frames    chan clientMessage // 容量 1，始终保留最新帧（背压：丢弃旧帧）
	closed    chan struct{}
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// run 阻塞直到会话结束。
func (sess *wsSession) run() {
	readerErr := make(chan error, 1)
	go sess.readLoop(readerErr)

	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case err := <-readerErr:
			_ = sess.close(err)
			return
		case <-sess.closed:
			return
		case msg := <-sess.frames:
			sess.processFrame(msg)
		case <-ticker.C:
			// 传输层心跳；对端 pong 会刷新读超时
			sess.writeControl(websocket.PingMessage)
		}
	}
}

// readLoop 读上行消息并分发，出错即结束会话。
func (sess *wsSession) readLoop(errCh chan<- error) {
	// base64 膨胀约 4/3，读上限留足余量
	sess.conn.SetReadLimit(int64(float64(sess.srv.opts.MaxFrameBytes)*1.4 + 64<<10))
	_ = sess.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	sess.conn.SetPongHandler(func(string) error {
		return sess.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	for {
		_, raw, err := sess.conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		_ = sess.conn.SetReadDeadline(time.Now().Add(wsPongWait))

		var msg clientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			sess.sendError(0, "BAD_MESSAGE", "invalid json")
			continue
		}
		switch msg.Type {
		case "hello":
			sess.sendJSON(serverMessage{
				Type:          "welcome",
				Protocol:      Protocol,
				Provider:      sess.srv.opts.Provider.Name(),
				KeypointNames: pose.KeypointNames,
			})
		case "ping":
			sess.sendJSON(serverMessage{Type: "pong", TS: msg.TS})
		case "frame":
			if msg.Format != "" && msg.Format != "jpeg" {
				sess.sendError(msg.Seq, "UNSUPPORTED_FORMAT", "only jpeg is supported")
				continue
			}
			sess.pushFrame(msg)
		default:
			sess.sendError(0, "BAD_MESSAGE", "unknown message type")
		}
	}
}

// pushFrame 只保留最新待处理帧：挤掉未处理的旧帧再入队。
func (sess *wsSession) pushFrame(msg clientMessage) {
	select {
	case sess.frames <- msg:
	default:
		select {
		case <-sess.frames:
		default:
		}
		select {
		case sess.frames <- msg:
		default:
		}
	}
}

// processFrame 单帧识别 + 分析 + 回包。
func (sess *wsSession) processFrame(msg clientMessage) {
	start := time.Now()
	frame, err := decodeFrame(msg, sess.srv.opts.MaxFrameBytes)
	if err != nil {
		switch {
		case errors.Is(err, errFrameTooLarge):
			sess.sendError(msg.Seq, "FRAME_TOO_LARGE", "frame exceeds limit")
		default:
			sess.sendError(msg.Seq, "BAD_FRAME", "base64 decode failed")
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), wsDetectWait)
	defer cancel()
	p, err := sess.srv.opts.Provider.Detect(ctx, frame)
	if err != nil {
		sess.sendError(msg.Seq, "INFERENCE_FAILED", err.Error())
		return
	}
	analysis, tips := sess.analyzer.Update(p)
	sess.sendJSON(serverMessage{
		Type:      "pose",
		Seq:       msg.Seq,
		Keypoints: p.Keypoints,
		Analysis:  &analysis,
		Tips:      tips,
		ServerMS:  time.Since(start).Milliseconds(),
	})
	sess.srv.log.Debug("frame processed", "seq", msg.Seq,
		"cost_ms", time.Since(start).Milliseconds())
}

// decodeFrame 校验大小并 base64 解码。
func decodeFrame(msg clientMessage, maxBytes int64) ([]byte, error) {
	if int64(len(msg.Data))*3/4 > maxBytes {
		return nil, errFrameTooLarge
	}
	frame, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil || len(frame) == 0 {
		return nil, errBadFrame
	}
	return frame, nil
}

// sendJSON 串行化数据帧写入。
func (sess *wsSession) sendJSON(msg serverMessage) {
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	_ = sess.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	_ = sess.conn.WriteJSON(msg)
}

// sendError 下发错误消息；detail 仅用于排查日志，客户端展示以 code 映射为准。
func (sess *wsSession) sendError(seq int64, code, detail string) {
	sess.srv.log.Warn("ws session error", "seq", seq, "code", code, "detail", detail)
	sess.sendJSON(serverMessage{Type: "error", Seq: seq, Code: code, Detail: detail})
}

// writeControl 发送协议层控制帧（可与数据帧并发）。
func (sess *wsSession) writeControl(msgType int) {
	_ = sess.conn.WriteControl(msgType, nil, time.Now().Add(wsWriteWait))
}

// close 关闭连接并通知会话退出。
func (sess *wsSession) close(err error) error {
	var closeErr error
	sess.closeOnce.Do(func() {
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			closeErr = sess.conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(wsWriteWait))
		}
		_ = sess.conn.Close()
		close(sess.closed)
	})
	return closeErr
}
