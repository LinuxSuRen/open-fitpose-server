package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/linuxsuren/open-fitpose-server/internal/api"
	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

func newTestServer(t *testing.T, opts api.Options) *httptest.Server {
	t.Helper()
	if opts.Provider == nil {
		opts.Provider = pose.NewMockProvider()
	}
	srv := api.NewServer(opts)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t, api.Options{})
	resp, err := http.Get(ts.URL + "/api/v1/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Status   string `json:"status"`
		Provider string `json:"provider"`
		Protocol int    `json:"protocol"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Status != "ok" || body.Provider != "mock" || body.Protocol != 1 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestDetectRawBody(t *testing.T) {
	ts := newTestServer(t, api.Options{})
	frame := []byte("fake-jpeg-bytes")
	resp, err := http.Post(ts.URL+"/api/v1/pose/detect", "image/jpeg", bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Keypoints []pose.Keypoint `json:"keypoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Keypoints) != len(pose.KeypointNames) {
		t.Fatalf("keypoints = %d, want %d", len(body.Keypoints), len(pose.KeypointNames))
	}
}

func TestDetectTooLarge(t *testing.T) {
	ts := newTestServer(t, api.Options{MaxFrameBytes: 1024})
	resp, err := http.Post(ts.URL+"/api/v1/pose/detect", "image/jpeg",
		bytes.NewReader(make([]byte, 2048)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestAuthReject(t *testing.T) {
	ts := newTestServer(t, api.Options{APIKey: "secret"})
	resp, err := http.Post(ts.URL+"/api/v1/pose/detect", "image/jpeg", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthAcceptWithToken(t *testing.T) {
	ts := newTestServer(t, api.Options{APIKey: "secret"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/pose/detect?token=secret",
		bytes.NewReader([]byte("x")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// dialWS 建立到 WS 端点的连接。
func dialWS(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	if token != "" {
		url += "?token=" + token
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func wsURL(ts *httptest.Server) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws/pose"
}

func recvMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode ws message: %v", err)
	}
	return msg
}

func TestWSSession(t *testing.T) {
	ts := newTestServer(t, api.Options{})
	conn := dialWS(t, wsURL(ts), "")

	// hello → welcome
	_ = conn.WriteJSON(map[string]any{"type": "hello", "protocol": 1})
	msg := recvMsg(t, conn)
	if msg["type"] != "welcome" {
		t.Fatalf("first message type = %v, want welcome", msg["type"])
	}
	if msg["provider"] != "mock" {
		t.Fatalf("provider = %v", msg["provider"])
	}
	names, _ := msg["keypoint_names"].([]any)
	if len(names) != len(pose.KeypointNames) {
		t.Fatalf("keypoint_names = %d", len(names))
	}

	// frame → pose（seq 回传，关键点齐全，带 analysis）
	frame := base64.StdEncoding.EncodeToString([]byte("fake-frame"))
	_ = conn.WriteJSON(map[string]any{"type": "frame", "seq": 42, "format": "jpeg", "data": frame})
	msg = recvMsg(t, conn)
	if msg["type"] != "pose" {
		t.Fatalf("message type = %v, want pose", msg["type"])
	}
	if int(msg["seq"].(float64)) != 42 {
		t.Fatalf("seq = %v, want 42", msg["seq"])
	}
	if kps, _ := msg["keypoints"].([]any); len(kps) != len(pose.KeypointNames) {
		t.Fatalf("keypoints = %d", len(kps))
	}
	if _, ok := msg["analysis"].(map[string]any); !ok {
		t.Fatalf("pose message missing analysis: %v", msg)
	}

	// ping → pong
	_ = conn.WriteJSON(map[string]any{"type": "ping", "ts": 12345})
	msg = recvMsg(t, conn)
	if msg["type"] != "pong" {
		t.Fatalf("message type = %v, want pong", msg["type"])
	}

	// 非法 json → error code
	_ = conn.WriteMessage(websocket.TextMessage, []byte("{not-json"))
	msg = recvMsg(t, conn)
	if msg["type"] != "error" || msg["code"] != "BAD_MESSAGE" {
		t.Fatalf("want BAD_MESSAGE error, got %v", msg)
	}

	// 非法 base64 → BAD_FRAME
	_ = conn.WriteJSON(map[string]any{"type": "frame", "seq": 43, "data": "!!!not-base64!!!"})
	msg = recvMsg(t, conn)
	if msg["type"] != "error" || msg["code"] != "BAD_FRAME" {
		t.Fatalf("want BAD_FRAME error, got %v", msg)
	}

	// 不支持的图像格式
	_ = conn.WriteJSON(map[string]any{"type": "frame", "seq": 44, "format": "png", "data": frame})
	msg = recvMsg(t, conn)
	if msg["type"] != "error" || msg["code"] != "UNSUPPORTED_FORMAT" {
		t.Fatalf("want UNSUPPORTED_FORMAT error, got %v", msg)
	}
}

func TestWSAuth(t *testing.T) {
	ts := newTestServer(t, api.Options{APIKey: "secret"})

	// 无 token：握手即被拒
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts), nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial without token should fail")
	}

	// 带 token：正常
	conn2 := dialWS(t, wsURL(ts), "secret")
	_ = conn2.WriteJSON(map[string]any{"type": "hello", "protocol": 1})
	msg := recvMsg(t, conn2)
	if msg["type"] != "welcome" {
		t.Fatalf("message type = %v, want welcome", msg["type"])
	}
}

func TestWSDetectError(t *testing.T) {
	// 注入始终失败的提供者，验证 INFERENCE_FAILED 透出为错误码
	ts := newTestServer(t, api.Options{Provider: failingProvider{}})
	conn := dialWS(t, wsURL(ts), "")

	_ = conn.WriteJSON(map[string]any{
		"type": "frame", "seq": 1,
		"data": base64.StdEncoding.EncodeToString([]byte("frame")),
	})
	msg := recvMsg(t, conn)
	if msg["type"] != "error" || msg["code"] != "INFERENCE_FAILED" {
		t.Fatalf("want INFERENCE_FAILED, got %v", msg)
	}
}

// failingProvider 始终返回错误的提供者。
type failingProvider struct{}

func (failingProvider) Name() string { return "mock" }

func (failingProvider) Detect(_ context.Context, _ []byte) (pose.Pose, error) {
	return pose.Pose{}, errors.New("inference backend down")
}
