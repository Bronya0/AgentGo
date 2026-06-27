// Package webui 提供内嵌的 Web UI 仪表盘。
//
// 使用 Go embed 将 HTML/CSS/JS 嵌入二进制文件，无需外部静态文件。
// 提供对话界面、工具可视化、会话管理、系统概览等功能。
//
// 路由：
//
//	GET /ui                   — Web UI 入口页面
//	GET /ui/static/...        — 嵌入式静态资源（JS/CSS/vendor）
//	GET /ui/api/info          — 系统信息 API
//	GET /ui/api/sessions      — 会话列表 API
//	DELETE /ui/api/sessions?id=xxx — 重置会话
//	GET /ui/api/workspaces    — 工作区列表（含会话）
//	PUT /ui/api/workspaces    — 添加工作区
//	DELETE /ui/api/workspaces?path=xxx — 移除工作区
//	PUT /ui/api/sessions      — 创建新会话
//	GET /ui/api/config        — 当前配置信息
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/bronya/mini-agent/internal/config"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
)

//go:embed static
var staticFiles embed.FS

// Server 是 Web UI 服务器。
type Server struct {
	sessions      *session.Pool
	tools         *tool.Registry
	config        *config.Config
	workspace     string // 当前工作区路径
	maxTokens     int    // runner 的 MaxTokens
	version       string
}

// New 创建一个 WebUI Server。
func New(sessions *session.Pool, tools *tool.Registry, cfg *config.Config, workspace string, maxTokens int, version string) *Server {
	return &Server{
		sessions:  sessions,
		tools:     tools,
		config:    cfg,
		workspace: workspace,
		maxTokens: maxTokens,
		version:   version,
	}
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
	mux.HandleFunc("/ui/api/workspaces", s.handleWorkspaces)
	mux.HandleFunc("/ui/api/config", s.handleConfig)
}

// handleIndex 返回单页应用 HTML（从嵌入 FS 读取）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
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

// handleSessions 返回会话列表或重置/创建会话。
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ids := s.sessions.ListIDs()
		type sessionInfo struct {
			ID            string `json:"id"`
			MessageCount  int    `json:"message_count"`
			TokenEstimate int    `json:"token_estimate"`
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

	case http.MethodPut:
		// 创建新会话：生成新的 session ID
		newID := fmt.Sprintf("session-%d", time.Now().UnixMilli())
		// 创建新 session 但不切换默认会话
		_ = s.sessions.Get(newID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": newID})

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

// workspaceInfo 是返回给前端的工作区数据结构。
type workspaceInfo struct {
	Path          string               `json:"path"`
	Name          string               `json:"name"`
	CurrentModel  string               `json:"current_model"`
	MaxTokens     int                  `json:"max_tokens"`
	Sessions      []session.SessionInfo `json:"sessions"`
}

// handleWorkspaces 处理工作区相关 API。
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListWorkspaces(w, r)
	case http.MethodPut:
		s.handleAddWorkspace(w, r)
	case http.MethodDelete:
		s.handleRemoveWorkspace(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// handleListWorkspaces 返回所有工作区及其会话列表。
func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	entries, err := LoadWorkspaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 确保当前工作区在列表中
	hasCurrent := false
	for _, e := range entries {
		if e.Path == s.workspace {
			hasCurrent = true
			break
		}
	}
	if !hasCurrent {
		entries = append([]WorkspaceEntry{{Path: s.workspace, AddedAt: time.Now().Format(time.RFC3339)}}, entries...)
	}

	// 获取当前模型名
	modelName := ""
	if len(s.config.Providers) > 0 {
		modelName = s.config.Providers[0].Model
	}

	workspaces := make([]workspaceInfo, 0, len(entries))
	for _, e := range entries {
		sessions, err := s.sessions.ListWorkspaceSessions(e.Path)
		if err != nil {
			// 工作区目录可能已不存在，跳过
			continue
		}
		workspaces = append(workspaces, workspaceInfo{
			Path:         e.Path,
			Name:         filepath.Base(e.Path),
			CurrentModel: modelName,
			MaxTokens:    s.maxTokens,
			Sessions:     sessions,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workspaces)
}

// handleAddWorkspace 手动添加工作区到索引。
func (s *Server) handleAddWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := AddWorkspace(req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleRemoveWorkspace 从索引中移除工作区。
func (s *Server) handleRemoveWorkspace(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := RemoveWorkspace(path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleConfig 返回当前配置信息。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	modelName := ""
	if len(s.config.Providers) > 0 {
		modelName = s.config.Providers[0].Model
	}

	info := map[string]any{
		"model":             modelName,
		"max_context_tokens": s.maxTokens,
		"workspace":         s.workspace,
		"workspace_name":    filepath.Base(s.workspace),
		"version":           s.version,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
