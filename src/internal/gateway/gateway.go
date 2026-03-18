// Package gateway 提供 HTTP API 服务器，接收用户消息并流式返回 agent 响应。
//
// 端点：
//
//	POST /v1/chat  — 发起对话（SSE 流式输出）
//	GET  /healthz  — 健康检查
//
// 认证：若配置了 token，须在 Authorization: Bearer <token> 头中提供。
//
// 请求体（JSON）：
//
//	{ "message": "...", "session_id": "optional" }
//
// SSE 响应格式（每行 "data: <JSON>\n\n"）：
//
//	{"type":"text","text":"..."}
//	{"type":"tool_start","tool":"...","args":"..."}
//	{"type":"tool_end","tool":"...","output":"..."}
//	{"type":"done"}
//	{"type":"error","error":"..."}
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bronya/mini-agent/internal/runner"
	"github.com/bronya/mini-agent/internal/session"
)

const maxRequestBodyBytes = 1 << 20 // 1 MB

// ChatRequest 是 POST /v1/chat 的请求体。
type ChatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

// Server 是 HTTP 网关服务器。
type Server struct {
	runner      *runner.Runner
	sessions    *session.Pool
	sessionLock *sessionLockMap
	token       string
	mux         *http.ServeMux
	httpSrv     *http.Server
}

// New 创建一个 Server。
func New(r *runner.Runner, sessions *session.Pool, addr, token string) *Server {
	s := &Server{
		runner:      r,
		sessions:    sessions,
		sessionLock: newSessionLockMap(),
		token:       token,
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/v1/chat", s.authMiddleware(s.handleChat))
	s.mux.HandleFunc("/v1/session/export", s.authMiddleware(s.handleExport))
	s.mux.HandleFunc("/v1/session/import", s.authMiddleware(s.handleImport))
	s.mux.HandleFunc("/healthz", handleHealthz)

	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 10 * time.Minute, // SSE 需要较长的写超时
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// Mux 返回底层 ServeMux，供外部模块（如 Channel）注册路由。
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Start 启动 HTTP 服务器（阻塞）。
func (s *Server) Start() error {
	slog.Info("gateway listening", "addr", s.httpSrv.Addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown 优雅关闭服务器，等待进行中的请求完成（最多 30 秒）。
func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}

// validSessionID 限制 session ID 为安全字符（字母数字、短横线、下划线）。
var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// handleChat 处理 POST /v1/chat 请求，以 SSE 流式输出 agent 响应。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制请求体大小
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "default"
	}
	// 验证 session ID 安全性（防路径穿越）
	if !validSessionID.MatchString(sessionID) {
		http.Error(w, "invalid session_id: only alphanumeric, hyphen, and underscore allowed (max 64 chars)", http.StatusBadRequest)
		return
	}

	sess := s.sessions.Get(sessionID)

	// 设置 SSE 响应头（必须在 WriteHeader 前设置）
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	send := func(eventType string, data map[string]any) {
		data["type"] = eventType
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if canFlush {
			flusher.Flush()
		}
	}

	// 获取 per-session 互斥令牌，保证同一 session 串行执行
	sr := s.sessionLock.get(sessionID)
	err := sr.run(r.Context(), func() {
		runErr := s.runner.Run(r.Context(), sess, req.Message, func(chunk runner.StreamChunk) {
			switch chunk.Event {
			case runner.EventText:
				send("text", map[string]any{"text": chunk.Text})
			case runner.EventToolStart:
				send("tool_start", map[string]any{"tool": chunk.ToolName, "args": chunk.ToolArgs})
			case runner.EventToolEnd:
				send("tool_end", map[string]any{"tool": chunk.ToolName, "output": chunk.ToolOut})
			case runner.EventDone:
				_ = sess.Save()
				send("done", map[string]any{})
			case runner.EventError:
				if chunk.Err != nil {
					send("error", map[string]any{"error": chunk.Err.Error()})
				}
			}
		})
		if runErr != nil {
			send("error", map[string]any{"error": runErr.Error()})
		}
	})
	if err != nil {
		slog.Info("session request cancelled", "session", sessionID, "err", err)
	}
}

// authMiddleware 实现 Bearer token 鉴权（constant-time 比较防时序攻击）。
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleHealthz 是健康检查端点。
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// --- 对话导出/导入 ---

// handleExport 处理 GET /v1/session/export?session_id=xxx&format=json|markdown
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

	format := session.ExportFormat(r.URL.Query().Get("format"))
	if format == "" {
		format = session.FormatJSON
	}
	if format != session.FormatJSON && format != session.FormatMarkdown {
		http.Error(w, "format must be json or markdown", http.StatusBadRequest)
		return
	}

	sess := s.sessions.Get(sessionID)
	content, err := sess.Export(format)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch format {
	case session.FormatMarkdown:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, sessionID))
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, sessionID))
	}
	fmt.Fprint(w, content)
}

// handleImport 处理 POST /v1/session/import?session_id=xxx
// 请求体为 JSON 格式的导出数据。
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or read error", http.StatusBadRequest)
		return
	}

	sess := s.sessions.Get(sessionID)
	if err := sess.Import(data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = sess.Save()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","session_id":%q}`, sessionID)
}

// --- per-session 串行执行控制 ---

// sessionRunner 保证同一 session 的请求串行执行。
type sessionRunner struct {
	token    chan struct{}
	lastUsed time.Time
}

func newSessionRunner() *sessionRunner {
	ch := make(chan struct{}, 1)
	ch <- struct{}{} // 令牌就绪
	return &sessionRunner{token: ch, lastUsed: time.Now()}
}

func (sr *sessionRunner) run(ctx context.Context, fn func()) error {
	select {
	case <-sr.token: // 获取令牌
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { sr.token <- struct{}{} }() // 归还令牌
	sr.lastUsed = time.Now()
	fn()
	return nil
}

// sessionLockMap 管理所有 session 的 sessionRunner（线程安全，含 GC）。
type sessionLockMap struct {
	mu      sync.Mutex
	runners map[string]*sessionRunner
}

func newSessionLockMap() *sessionLockMap {
	m := &sessionLockMap{runners: make(map[string]*sessionRunner)}
	go m.gc()
	return m
}

func (m *sessionLockMap) get(id string) *sessionRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sr, ok := m.runners[id]; ok {
		return sr
	}
	sr := newSessionRunner()
	m.runners[id] = sr
	return sr
}

// gc 每 10 分钟清理 30 分钟未使用的 session lock，防止内存无限增长。
func (m *sessionLockMap) gc() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		m.mu.Lock()
		for id, sr := range m.runners {
			if time.Since(sr.lastUsed) > 30*time.Minute {
				delete(m.runners, id)
			}
		}
		m.mu.Unlock()
	}
}
