// Package api 提供 HTTP 服务：健康检查、单帧识别 REST 接口、
// WebSocket 实时流式识别（见 docs/protocol.md）与内嵌 Web 前端。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/linuxsuren/open-fitpose-server/internal/analyze"
	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// Protocol WS 协议版本，客户端 hello/welcome 时核对。
const Protocol = 1

var (
	errFrameTooLarge = errors.New("frame too large")
	errBadFrame      = errors.New("bad frame")
)

// Options 服务配置。
type Options struct {
	// Provider 姿态推理提供者（必填）。
	Provider pose.Provider
	// WebFS 内嵌 Web 前端（可选，为空则不注册静态页）。
	WebFS fs.FS
	// APIKey 非空时所有接口要求 Bearer Token 或 query token，空则不鉴权。
	APIKey string
	// MaxFrameBytes 单帧图像的最大字节数。
	MaxFrameBytes int64
	// Logger 日志器，为空用 slog 默认。
	Logger *slog.Logger
}

// Server HTTP 服务。
type Server struct {
	opts Options
	log  *slog.Logger
}

// NewServer 创建服务。
func NewServer(opts Options) *Server {
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = 3 << 20 // 3 MiB
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{opts: opts, log: log}
}

// Handler 组装路由与中间件。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/v1/pose/detect", s.auth(s.handleDetect))
	mux.HandleFunc("GET /api/v1/ws/pose", s.auth(s.handleWS))

	if s.opts.WebFS != nil {
		mux.Handle("GET /", http.FileServerFS(s.opts.WebFS))
	}
	return s.logAccess(mux)
}

// logAccess 访问日志（静态资源降级为 debug，避免刷屏）。
// WebSocket 路径不包装 ResponseWriter，避免丢失 http.Hijacker 导致升级失败。
func (s *Server) logAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/ws/pose" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		lvl := slog.LevelInfo
		if r.URL.Path == "/" || strings.Contains(r.URL.Path, ".") {
			lvl = slog.LevelDebug
		}
		s.log.Log(r.Context(), lvl, "http access",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "cost_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// auth 可选 API Key 鉴权：Authorization: Bearer <key> 或 ?token=<key>。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIKey != "" {
			got := ""
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				got = strings.TrimPrefix(h, "Bearer ")
			} else if q := r.URL.Query().Get("token"); q != "" {
				got = q
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.APIKey)) != 1 {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid token")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"provider": s.opts.Provider.Name(),
		"protocol": Protocol,
	})
}

// handleDetect 单帧识别：multipart 字段 frame，或请求体直接为图像字节。
// 用于冒烟测试与一次性识别；实时场景请走 WebSocket。
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	frame, err := readFrameHTTP(r, s.opts.MaxFrameBytes)
	if err != nil {
		s.writeFrameError(w, err)
		return
	}
	start := time.Now()
	p, err := s.opts.Provider.Detect(r.Context(), frame)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INFERENCE_FAILED", err.Error())
		return
	}
	an := analyze.NewAnalyzer()
	analysis, tips := an.Update(p)
	s.log.Debug("detect done", "cost_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"keypoints": p.Keypoints,
		"analysis":  analysis,
		"tips":      tips,
		"server_ms": time.Since(start).Milliseconds(),
	})
}

// readFrameHTTP 从 multipart 字段 frame 或原始请求体读取图像，并校验大小。
func readFrameHTTP(r *http.Request, maxBytes int64) ([]byte, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxBytes); err != nil {
			return nil, errBadFrame
		}
		f, _, err := r.FormFile("frame")
		if err != nil {
			return nil, errors.Join(errBadFrame, errors.New("multipart field 'frame' required"))
		}
		defer f.Close()
		return readLimited(f, maxBytes)
	}
	return readLimited(r.Body, maxBytes)
}

func (s *Server) writeFrameError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFrameTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "FRAME_TOO_LARGE", "frame exceeds limit")
	case errors.Is(err, errBadFrame):
		writeError(w, http.StatusBadRequest, "BAD_FRAME", "cannot read frame")
	default:
		writeError(w, http.StatusBadRequest, "BAD_FRAME", "cannot read frame")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{"code": code, "detail": detail})
}

// readLimited 读取并校验大小上限。
func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, errBadFrame
	}
	if int64(len(data)) > maxBytes {
		return nil, errFrameTooLarge
	}
	return data, nil
}
