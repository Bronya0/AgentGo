// Package memory 实现简易文本记忆存储与检索。
//
// 不依赖向量数据库，使用基于关键词的文本检索。
// 适合 mini-agent 的轻量级使用场景。
package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry 是一条记忆条目。
type Entry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 管理记忆条目的存储和检索。
type Store struct {
	mu      sync.RWMutex
	entries []Entry
	dir     string // 持久化目录
	nextID  int
}

// NewStore 创建一个记忆存储。dir 为持久化目录，为空则不持久化。
func NewStore(dir string) *Store {
	s := &Store{dir: dir}
	if dir != "" {
		s.load()
	}
	return s
}

// Add 添加一条记忆。
func (s *Store) Add(content string, tags ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	entry := Entry{
		ID:        fmt.Sprintf("mem_%d", s.nextID),
		Content:   content,
		Tags:      tags,
		CreatedAt: time.Now(),
	}
	s.entries = append(s.entries, entry)
	s.save()
	return entry.ID
}

// Search 根据关键词搜索记忆（简单子串匹配）。
// 返回最多 maxResults 条匹配结果，按相关性（匹配词数）降序排列。
func (s *Store) Search(query string, maxResults int) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if maxResults <= 0 {
		maxResults = 6
	}

	queryLower := strings.ToLower(query)
	words := strings.Fields(queryLower)

	type scored struct {
		entry Entry
		score int
	}
	var matches []scored

	for _, e := range s.entries {
		contentLower := strings.ToLower(e.Content)
		tagsLower := strings.ToLower(strings.Join(e.Tags, " "))
		combined := contentLower + " " + tagsLower

		score := 0
		for _, w := range words {
			if strings.Contains(combined, w) {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, scored{entry: e, score: score})
		}
	}

	// 按 score 降序排序（简单冒泡）
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	result := make([]Entry, 0, maxResults)
	for i := 0; i < len(matches) && i < maxResults; i++ {
		result = append(result, matches[i].entry)
	}
	return result
}

// All 返回所有记忆条目。
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *Store) save() {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		slog.Error("memory save: mkdir", "err", err)
		return
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		slog.Error("memory save: marshal", "err", err)
		return
	}
	if err := os.WriteFile(filepath.Join(s.dir, "memory.json"), data, 0o600); err != nil {
		slog.Error("memory save: write", "err", err)
	}
}

func (s *Store) load() {
	fp := filepath.Join(s.dir, "memory.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &s.entries)
	// 用实际最大 ID 编号来避免碰撞
	for _, e := range s.entries {
		var n int
		if _, err := fmt.Sscanf(e.ID, "mem_%d", &n); err == nil && n > s.nextID {
			s.nextID = n
		}
	}
}
