// Package session 管理对话会话（消息历史 + 持久化）。
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bronya/mini-agent/internal/provider"
)

// Session 是一个对话会话，持有完整消息历史。
type Session struct {
	ID       string             `json:"id"`
	Messages []provider.Message `json:"messages"`
	Summary  string             `json:"summary,omitempty"` // 压缩后的早期上下文摘要
	mu       sync.Mutex
	filePath string // 持久化路径（空表示不持久化）
}

// New 创建一个新会话。
func New(id string) *Session {
	return &Session{ID: id}
}

// Append 追加一条消息。
func (s *Session) Append(msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
}

// History 返回消息历史副本。
func (s *Session) History() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// TokenEstimate 返回消息的 token 估算值（chars/4 启发式）。
func (s *Session) TokenEstimate() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, m := range s.Messages {
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Arguments) / 4
		}
	}
	return total
}

// Reset 清空消息历史。
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	s.Summary = ""
}

// Compress 将前 keepFromIndex 条消息的摘要存储，并丢弃这些消息。
func (s *Session) Compress(summaryText string, keepFromIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keepFromIndex <= 0 || keepFromIndex > len(s.Messages) {
		return
	}
	// 如有旧摘要，合并
	if s.Summary != "" {
		s.Summary = s.Summary + "\n\n" + summaryText
	} else {
		s.Summary = summaryText
	}
	s.Messages = append([]provider.Message(nil), s.Messages[keepFromIndex:]...)
}

// GetSummary 返回已压缩的上下文摘要。
func (s *Session) GetSummary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Summary
}

// Save 将会话持久化到磁盘（若已配置路径）。
func (s *Session) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.filePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0o600)
}

// SetFilePath 设置持久化路径。
func (s *Session) SetFilePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filePath = path
}

// Pool 管理多个 Session（线程安全）。
type Pool struct {
	mu       sync.Mutex
	sessions map[string]*Session
	dataDir  string // 持久化目录
}

// NewPool 创建一个 session 池。dataDir 为空表示不做持久化。
func NewPool(dataDir string) *Pool {
	return &Pool{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
	}
}

// Get 获取或创建一个 session。
func (p *Pool) Get(id string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s, ok := p.sessions[id]; ok {
		return s
	}

	s := New(id)
	if p.dataDir != "" {
		fp := filepath.Join(p.dataDir, id+".json")
		s.SetFilePath(fp)
		// 尝试从磁盘恢复
		if data, err := os.ReadFile(fp); err == nil {
			_ = json.Unmarshal(data, s)
		}
	}
	p.sessions[id] = s
	return s
}

// ListIDs 返回所有活跃和已持久化会话的 ID 列表。
func (p *Pool) ListIDs() []string {
	p.mu.Lock()
	seen := make(map[string]bool, len(p.sessions))
	ids := make([]string, 0, len(p.sessions))
	for id := range p.sessions {
		seen[id] = true
		ids = append(ids, id)
	}
	dataDir := p.dataDir
	p.mu.Unlock()

	if dataDir != "" {
		entries, err := os.ReadDir(dataDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".json")
				if !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
		}
	}
	return ids
}

// SessionInfo 是会话的摘要信息（用于 UI 列表展示）。
type SessionInfo struct {
	ID            string    `json:"id"`
	MessageCount  int       `json:"message_count"`
	TokenEstimate int       `json:"token_estimate"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ListWorkspaceSessions 列出指定工作区目录下的所有会话摘要信息。
// 不将这些会话加载到 Pool 的内存映射中。
func (p *Pool) ListWorkspaceSessions(workspacePath string) ([]SessionInfo, error) {
	sessionsDir := filepath.Join(workspacePath, ".agent", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionInfo{}, nil
		}
		return nil, err
	}

	var list []SessionInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		fp := filepath.Join(sessionsDir, e.Name())

		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}

		info, err := os.Stat(fp)
		modTime := time.Now()
		if err == nil {
			modTime = info.ModTime()
		}

		list = append(list, SessionInfo{
			ID:            id,
			MessageCount:  len(s.Messages),
			TokenEstimate: s.TokenEstimate(),
			UpdatedAt:     modTime,
		})
	}

	// 按更新时间倒序
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].UpdatedAt.After(list[i].UpdatedAt) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}

	return list, nil
}

// Delete 删除指定会话（从内存和磁盘移除）。
func (p *Pool) Delete(id string) {
	p.mu.Lock()
	s, ok := p.sessions[id]
	if ok {
		delete(p.sessions, id)
	}
	p.mu.Unlock()

	if ok && p.dataDir != "" {
		fp := filepath.Join(p.dataDir, id+".json")
		_ = os.Remove(fp)
	}
	_ = s // avoid unused
}

// Expire 删除超过 maxAge 未活动的会话。返回被删除的会话数量。
func (p *Pool) Expire(maxAge time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	var toDelete []string
	for id := range p.sessions {
		if p.dataDir != "" {
			fp := filepath.Join(p.dataDir, id+".json")
			info, err := os.Stat(fp)
			if err == nil && info.ModTime().Before(cutoff) {
				toDelete = append(toDelete, id)
			}
		}
	}
	for _, id := range toDelete {
		delete(p.sessions, id)
		if p.dataDir != "" {
			_ = os.Remove(filepath.Join(p.dataDir, id+".json"))
		}
	}
	return len(toDelete)
}
