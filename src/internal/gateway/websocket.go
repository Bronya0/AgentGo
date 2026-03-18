// Package gateway — WebSocket 端点，支持双向实时对话。
//
// 端点：GET /v1/ws?session_id=xxx
//
// 客户端消息帧（JSON）：
//
//	{"type":"message","message":"..."}  — 发送消息
//	{"type":"abort"}                    — 中止当前生成
//
// 服务端推送帧（JSON）：
//
//	{"type":"text","text":"..."}
//	{"type":"tool_start","tool":"...","args":"..."}
//	{"type":"tool_end","tool":"...","output":"..."}
//	{"type":"done"}
//	{"type":"aborted"}
//	{"type":"error","error":"..."}
package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bronya/mini-agent/internal/runner"
)

const (
	wsMaxMessageBytes = 1 << 20         // 1 MB 单帧上限
	wsPongWait        = 60 * time.Second // 等待 Pong 超时
	wsPingPeriod      = 50 * time.Second // 发送 Ping 间隔（< wsPongWait）
	wsWriteWait       = 10 * time.Second // 写操作超时
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 允许所有来源（内嵌 UI 与 API 共享同一 host，无跨域问题）
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleWebSocket 处理 GET /v1/ws WebSocket 升级请求。
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = "default"
	}
	if !validSessionID.MatchString(sessionID) {
		http.Error(w, "invalid session_id", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "session", sessionID, "err", err)
		return
	}
	defer conn.Close()

	// 安全配置
	conn.SetReadLimit(wsMaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	slog.Info("websocket connected", "session", sessionID, "remote", conn.RemoteAddr())
	defer slog.Info("websocket disconnected", "session", sessionID)

	sess := s.sessions.Get(sessionID)

	// gorilla/websocket 写操作非并发安全，用 mutex 保护
	var writeMu sync.Mutex
	sendJSON := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		if err := conn.WriteJSON(v); err != nil {
			slog.Debug("websocket write error", "session", sessionID, "err", err)
		}
	}

	// 心跳 goroutine：定期发 Ping，防止连接被中间设备超时断开
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// 当前任务的取消函数，用于支持中止
	var cancelMu sync.Mutex
	var cancelCurrent context.CancelFunc

	for {
		_, p, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("websocket read error", "session", sessionID, "err", err)
			}
			return
		}

		var ctrl struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(p, &ctrl); err != nil {
			sendJSON(map[string]any{"type": "error", "error": "invalid JSON"})
			continue
		}

		// 中止指令
		if ctrl.Type == "abort" {
			cancelMu.Lock()
			if cancelCurrent != nil {
				cancelCurrent()
				cancelCurrent = nil
			}
			cancelMu.Unlock()
			sendJSON(map[string]any{"type": "aborted"})
			continue
		}

		// 普通聊天消息
		msg := ctrl.Message
		if ctrl.Type != "" && ctrl.Type != "message" {
			// 未知类型，忽略
			continue
		}
		if msg == "" {
			sendJSON(map[string]any{"type": "error", "error": "empty message"})
			continue
		}

		cancelMu.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		cancelCurrent = cancel
		cancelMu.Unlock()

		runErr := s.runner.Run(ctx, sess, msg, func(chunk runner.StreamChunk) {
			if ctx.Err() != nil {
				return // 已中止，不再推送
			}
			switch chunk.Event {
			case runner.EventText:
				sendJSON(map[string]any{"type": "text", "text": chunk.Text})
			case runner.EventToolStart:
				sendJSON(map[string]any{"type": "tool_start", "tool": chunk.ToolName, "args": chunk.ToolArgs})
			case runner.EventToolEnd:
				sendJSON(map[string]any{"type": "tool_end", "tool": chunk.ToolName, "output": chunk.ToolOut})
			case runner.EventDone:
				_ = sess.Save()
				sendJSON(map[string]any{"type": "done"})
			case runner.EventError:
				if chunk.Err != nil {
					sendJSON(map[string]any{"type": "error", "error": chunk.Err.Error()})
				}
			}
		})

		cancelMu.Lock()
		cancelCurrent = nil
		cancelMu.Unlock()

		if runErr != nil && ctx.Err() == nil {
			sendJSON(map[string]any{"type": "error", "error": runErr.Error()})
		}
	}
}
