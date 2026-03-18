// Package webui 提供内嵌的 Web UI 仪表盘。
//
// 使用 Go embed 将 HTML/CSS/JS 嵌入二进制文件，无需外部静态文件。
// 提供对话界面、工具可视化、会话管理、系统概览等功能。
//
// 路由：
//
//	GET /ui              — Web UI 入口页面
//	GET /ui/static/...   — 嵌入式静态资源（JS/CSS/vendor）
//	GET /ui/api/info     — 系统信息 API
//	GET /ui/api/sessions — 会话列表 API
//	DELETE /ui/api/sessions?id=xxx — 重置会话
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
)

//go:embed static
var staticFiles embed.FS

// Server 是 Web UI 服务器。
type Server struct {
	sessions *session.Pool
	tools    *tool.Registry
	version  string
}

// New 创建一个 WebUI Server。
func New(sessions *session.Pool, tools *tool.Registry, version string) *Server {
	return &Server{sessions: sessions, tools: tools, version: version}
}

// RegisterRoutes 将 Web UI 路由注册到 ServeMux。
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// 静态资源：从嵌入 FS 中剥离 "static" 前缀后挂到 /ui/static/
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("webui: failed to sub static FS: " + err.Error())
	}
	mux.Handle("/ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(sub))))

	mux.HandleFunc("/ui", s.handleIndex)
	mux.HandleFunc("/ui/", s.handleIndex)
	mux.HandleFunc("/ui/api/info", s.handleInfo)
	mux.HandleFunc("/ui/api/sessions", s.handleSessions)
}

// handleIndex 返回单页应用 HTML（从嵌入 FS 读取）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// /ui/api/* 子路由已先注册，此处只处理真正的页面请求
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// handleInfo 返回系统概览信息。
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	tools := s.tools.List()
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Name
	}

	info := map[string]any{
		"version":    s.version,
		"tool_count": len(tools),
		"tools":      toolNames,
		"sessions":   s.sessions.ListIDs(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleSessions 返回会话列表或重置会话。
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ids := s.sessions.ListIDs()
		type sessionInfo struct {
			ID           string `json:"id"`
			MessageCount int    `json:"message_count"`
			TokenEstimate int   `json:"token_estimate"`
		}
		list := make([]sessionInfo, 0, len(ids))
		for _, id := range ids {
			sess := s.sessions.Get(id)
			list = append(list, sessionInfo{
				ID:            id,
				MessageCount:  len(sess.History()),
				TokenEstimate: sess.TokenEstimate(),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		sess := s.sessions.Get(id)
		sess.Reset()
		_ = sess.Save()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
