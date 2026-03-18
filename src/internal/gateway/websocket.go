// Package gateway — WebSocket 端点，支持双向实时对话。
//
// 端点：GET /v1/ws?session_id=xxx
//
// 握手后客户端发送 JSON 文本帧：{"message":"..."}
// 服务端推送 JSON 文本帧，格式与 SSE 事件相同：
//   {"type":"text","text":"..."}
//   {"type":"tool_start","tool":"...","args":"..."}
//   {"type":"tool_end","tool":"...","output":"..."}
//   {"type":"done"}
//   {"type":"error","error":"..."}
//
// 使用纯标准库实现 WebSocket（RFC 6455），无需外部依赖。
package gateway

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/bronya/mini-agent/internal/runner"
)

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-5AB9E5AF665B"

// WebSocket opcode
const (
	wsOpText  = 1
	wsOpClose = 8
	wsOpPing  = 9
	wsOpPong  = 10
)

// wsConn 封装 WebSocket 连接的读写操作。
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex // 保护写操作
	closed bool
}

// handleWebSocket 处理 GET /v1/ws WebSocket 升级请求。
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证 WebSocket 升级头
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "Not a WebSocket request", http.StatusBadRequest)
		return
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "Missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = "default"
	}
	if !validSessionID.MatchString(sessionID) {
		http.Error(w, "invalid session_id", http.StatusBadRequest)
		return
	}

	// 执行 WebSocket 握手
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket not supported", http.StatusInternalServerError)
		return
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		slog.Error("websocket hijack failed", "err", err)
		return
	}

	// 发送 101 Switching Protocols
	acceptKey := wsAcceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return
	}

	ws := &wsConn{conn: conn, br: bufrw.Reader}
	slog.Info("websocket connected", "session", sessionID, "remote", conn.RemoteAddr())

	// 进入消息循环
	s.wsLoop(ws, sessionID)
}

// wsLoop 是 WebSocket 消息处理主循环。
func (s *Server) wsLoop(ws *wsConn, sessionID string) {
	defer func() {
		ws.close()
		slog.Info("websocket disconnected", "session", sessionID)
	}()

	sess := s.sessions.Get(sessionID)

	for {
		op, payload, err := ws.readFrame()
		if err != nil {
			if err != io.EOF {
				slog.Debug("websocket read error", "err", err)
			}
			return
		}

		switch op {
		case wsOpClose:
			ws.writeFrame(wsOpClose, nil)
			return

		case wsOpPing:
			ws.writeFrame(wsOpPong, payload)

		case wsOpText:
			var req struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload, &req); err != nil || req.Message == "" {
				ws.sendJSON(map[string]any{"type": "error", "error": "invalid message format"})
				continue
			}

			// 使用 background context（WebSocket 连接生命周期由帧读取控制）
			ctx := context.Background()
			runErr := s.runner.Run(ctx, sess, req.Message, func(chunk runner.StreamChunk) {
				switch chunk.Event {
				case runner.EventText:
					ws.sendJSON(map[string]any{"type": "text", "text": chunk.Text})
				case runner.EventToolStart:
					ws.sendJSON(map[string]any{"type": "tool_start", "tool": chunk.ToolName, "args": chunk.ToolArgs})
				case runner.EventToolEnd:
					ws.sendJSON(map[string]any{"type": "tool_end", "tool": chunk.ToolName, "output": chunk.ToolOut})
				case runner.EventDone:
					_ = sess.Save()
					ws.sendJSON(map[string]any{"type": "done"})
				case runner.EventError:
					if chunk.Err != nil {
						ws.sendJSON(map[string]any{"type": "error", "error": chunk.Err.Error()})
					}
				}
			})
			if runErr != nil {
				ws.sendJSON(map[string]any{"type": "error", "error": runErr.Error()})
			}
		}
	}
}

// --- WebSocket 帧处理 ---

// readFrame 读取一个完整的 WebSocket 帧（支持掩码）。
func (ws *wsConn) readFrame() (opcode byte, payload []byte, err error) {
	// 头 2 字节
	header := make([]byte, 2)
	if _, err = io.ReadFull(ws.br, header); err != nil {
		return 0, nil, err
	}

	opcode = header[0] & 0x0F
	masked := (header[1] & 0x80) != 0
	length := uint64(header[1] & 0x7F)

	// 扩展长度
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(ws.br, ext); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(ws.br, ext); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext)
	}

	// 限制帧大小（1MB）
	if length > 1<<20 {
		return 0, nil, fmt.Errorf("frame too large: %d", length)
	}

	// 掩码
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(ws.br, mask[:]); err != nil {
			return
		}
	}

	// 载荷
	payload = make([]byte, length)
	if _, err = io.ReadFull(ws.br, payload); err != nil {
		return
	}

	// 解掩码
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	return opcode, payload, nil
}

// writeFrame 发送一个 WebSocket 帧（服务端不掩码）。
func (ws *wsConn) writeFrame(opcode byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return fmt.Errorf("connection closed")
	}

	// 构建帧头
	var buf []byte
	buf = append(buf, 0x80|opcode) // FIN + opcode

	length := len(payload)
	switch {
	case length <= 125:
		buf = append(buf, byte(length))
	case length <= 65535:
		buf = append(buf, 126)
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(length))
		buf = append(buf, ext...)
	default:
		buf = append(buf, 127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(length))
		buf = append(buf, ext...)
	}

	buf = append(buf, payload...)
	_, err := ws.conn.Write(buf)
	return err
}

// sendJSON 序列化并发送 JSON 文本帧。
func (ws *wsConn) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = ws.writeFrame(wsOpText, data)
}

// close 关闭连接。
func (ws *wsConn) close() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if !ws.closed {
		ws.closed = true
		ws.conn.Close()
	}
}

// wsAcceptKey 计算 Sec-WebSocket-Accept 响应值。
func wsAcceptKey(clientKey string) string {
	h := sha1.New()
	h.Write([]byte(clientKey + wsMagicGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
