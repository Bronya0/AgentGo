// Package checkpoint 实现"每轮对话文件快照"方案的回滚功能。
//
// 设计参考：
//   - Aider 用 git commit 每轮打点，回滚用 git reset
//   - Claude Code 拦截 write 工具前自动备份，回滚时还原备份
//
// 我们选 Claude Code 式的文件快照方案，不依赖 git，优点：
//   - 非 git 项目也能用
//   - 不污染用户的工作树提交历史
//   - 快速（只备份被改动的文件，不是整个目录）
//
// 工作流程：
//  1. Runner 在调用 write_file / edit_file / run_command 前，通过 hook
//     通知 checkpoint：这个文件即将被改（PreWrite）
//  2. checkpoint 首次看到这个文件（在当前 turn 内）时，复制到 .agent/checkpoints/<turn-id>/
//  3. 一轮对话结束后（用户下次输入前）调用 Seal，把 metadata 存盘
//  4. 用户 /rollback 时列出所有 turn，选一个 → 逐个还原文件内容
package checkpoint

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry 是一个文件快照。
type Entry struct {
	RelPath  string    `json:"rel_path"` // 相对工作区的路径
	BackupFP string    `json:"backup"`   // 备份文件在 .agent/checkpoints/.../files/ 下的相对路径
	Existed  bool      `json:"existed"`  // 改动前是否存在（用于还原时判断该恢复还是删除）
	At       time.Time `json:"at"`
}

// Turn 是一轮对话的快照集合。
type Turn struct {
	ID        string    `json:"id"`         // 时间戳 id，如 "20260510T153012-001"
	CreatedAt time.Time `json:"created_at"`
	UserInput string    `json:"user_input"` // 本轮用户输入摘要（首行，最多 200 字符）
	Entries   []Entry   `json:"entries"`    // 本轮备份的所有文件
	Dir       string    `json:"-"`          // turn 目录的绝对路径
}

// Manager 负责管理所有 checkpoint。
type Manager struct {
	workspace string
	rootDir   string // .agent/checkpoints

	mu        sync.Mutex
	current   *Turn // 当前开启中的 turn，nil 表示还没开始
	seen      map[string]bool
	turns     []*Turn // 已 Seal 的 turn 列表（只在启动时加载一次）
}

// New 创建一个 Manager。workspace 必须是绝对路径。
func New(workspace string) *Manager {
	root := filepath.Join(workspace, ".agent", "checkpoints")
	m := &Manager{workspace: workspace, rootDir: root}
	m.loadTurns()
	return m
}

// BeginTurn 标记一轮新对话开始。userInput 用于回滚列表展示。
func (m *Manager) BeginTurn(userInput string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := time.Now().Format("20060102T150405")
	// 添加毫秒避免冲突
	id += fmt.Sprintf("-%03d", time.Now().Nanosecond()/1_000_000)
	turnDir := filepath.Join(m.rootDir, id)

	input := strings.SplitN(strings.TrimSpace(userInput), "\n", 2)[0]
	runes := []rune(input)
	if len(runes) > 200 {
		input = string(runes[:200]) + "…"
	}

	m.current = &Turn{
		ID:        id,
		CreatedAt: time.Now(),
		UserInput: input,
		Dir:       turnDir,
	}
	m.seen = make(map[string]bool)
}

// SnapshotFile 在文件即将被修改前调用，记录原始内容。
// 同一 turn 内同一文件只备份一次（以第一次改动前的状态为准）。
// relPath 是相对工作区的路径。
func (m *Manager) SnapshotFile(relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return nil // 还没有 turn，略过
	}
	if m.seen[relPath] {
		return nil // 本 turn 已备份过
	}
	m.seen[relPath] = true

	srcPath := filepath.Join(m.workspace, relPath)
	existed := true
	info, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			existed = false
		} else {
			return err
		}
	} else if info.IsDir() {
		// 目录不备份
		return nil
	}

	entry := Entry{
		RelPath:  relPath,
		BackupFP: filepath.ToSlash(relPath),
		Existed:  existed,
		At:       time.Now(),
	}

	if existed {
		backupPath := filepath.Join(m.current.Dir, "files", relPath)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
			return err
		}
		if err := copyFile(srcPath, backupPath); err != nil {
			return err
		}
	}

	m.current.Entries = append(m.current.Entries, entry)
	return nil
}

// SealTurn 结束当前 turn，写入 metadata。
// 如果本轮没有任何文件改动，整个 turn 目录会被删除。
func (m *Manager) SealTurn() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return
	}
	if len(m.current.Entries) == 0 {
		// 没有改动，不保留
		m.current = nil
		return
	}

	_ = os.MkdirAll(m.current.Dir, 0o755)
	metaPath := filepath.Join(m.current.Dir, "meta.json")
	data, _ := json.MarshalIndent(m.current, "", "  ")
	_ = os.WriteFile(metaPath, data, 0o600)

	m.turns = append(m.turns, m.current)
	m.current = nil
}

// List 返回所有已 sealed 的 turn，按时间倒序（最新在前）。
func (m *Manager) List() []*Turn {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Turn, len(m.turns))
	copy(out, m.turns)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Rollback 回滚到指定的 turn（含该 turn 本身 — 即还原该轮"改动前"的状态）。
// 所有 turnID 之后（含）的 turn 都会逐一还原。
func (m *Manager) Rollback(turnID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 找到目标 turn 的索引
	targetIdx := -1
	for i, t := range m.turns {
		if t.ID == turnID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return 0, fmt.Errorf("turn %q not found", turnID)
	}

	// 从最新的 turn 倒序还原，直到目标 turn（含）
	// 因为每个 turn 记录的是"改动前"的状态，倒序还原可以得到目标 turn 开始时的工作区
	filesRestored := 0
	for i := len(m.turns) - 1; i >= targetIdx; i-- {
		t := m.turns[i]
		for _, e := range t.Entries {
			if err := m.restoreEntry(t, e); err != nil {
				return filesRestored, fmt.Errorf("restore %s: %w", e.RelPath, err)
			}
			filesRestored++
		}
	}

	// 删除这些 turn 的快照目录与内存记录
	for i := len(m.turns) - 1; i >= targetIdx; i-- {
		_ = os.RemoveAll(m.turns[i].Dir)
	}
	m.turns = m.turns[:targetIdx]

	return filesRestored, nil
}

// restoreEntry 把一个 entry 还原到工作区。
func (m *Manager) restoreEntry(turn *Turn, e Entry) error {
	dst := filepath.Join(m.workspace, e.RelPath)
	if !e.Existed {
		// 原本不存在 → 删除
		_ = os.Remove(dst)
		// 尝试清理空目录
		_ = removeEmptyParents(dst, m.workspace)
		return nil
	}
	backup := filepath.Join(turn.Dir, "files", e.RelPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(backup, dst)
}

// loadTurns 从磁盘扫描 checkpoints 目录，按时间顺序加载。
func (m *Manager) loadTurns() {
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(m.rootDir, e.Name(), "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var t Turn
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		t.Dir = filepath.Join(m.rootDir, e.Name())
		m.turns = append(m.turns, &t)
	}
	sort.Slice(m.turns, func(i, j int) bool { return m.turns[i].CreatedAt.Before(m.turns[j].CreatedAt) })
}

// --- helpers ---

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	d, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}

// removeEmptyParents 逐级向上删空目录（直到遇到 workspace 或非空目录）。
func removeEmptyParents(path, stopAt string) error {
	dir := filepath.Dir(path)
	for dir != stopAt && dir != filepath.Dir(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return nil
		}
		_ = os.Remove(dir)
		dir = filepath.Dir(dir)
	}
	return nil
}
