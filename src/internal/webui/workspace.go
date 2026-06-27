// Package webui — 工作区索引管理。
//
// 维护一个用户级的工作区注册表，记录用户通过 Web UI 访问过的所有工作区目录。
// 注册表存储在 ~/.agent/workspaces.json 中。
package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WorkspaceEntry 描述一个已注册的工作区。
type WorkspaceEntry struct {
	Path    string `json:"path"`
	AddedAt string `json:"added_at"`
}

// workspacesPath 返回工作区索引文件的路径。
func workspacesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create .agent dir: %w", err)
	}
	return filepath.Join(dir, "workspaces.json"), nil
}

// LoadWorkspaces 从 ~/.agent/workspaces.json 加载工作区列表。
// 若文件不存在，返回空列表。
func LoadWorkspaces() ([]WorkspaceEntry, error) {
	path, err := workspacesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorkspaceEntry{}, nil
		}
		return nil, fmt.Errorf("read workspaces: %w", err)
	}
	var list []WorkspaceEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse workspaces: %w", err)
	}
	return list, nil
}

// SaveWorkspaces 将工作区列表持久化到 ~/.agent/workspaces.json。
func SaveWorkspaces(list []WorkspaceEntry) error {
	path, err := workspacesPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspaces: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write workspaces: %w", err)
	}
	return nil
}

// AddWorkspace 将工作区路径添加到索引中（若已存在则更新 added_at）。
func AddWorkspace(workspacePath string) error {
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	list, err := LoadWorkspaces()
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	for i, w := range list {
		if w.Path == abs {
			list[i].AddedAt = now
			return SaveWorkspaces(list)
		}
	}
	list = append(list, WorkspaceEntry{Path: abs, AddedAt: now})
	return SaveWorkspaces(list)
}

// RemoveWorkspace 从索引中移除指定工作区。
func RemoveWorkspace(workspacePath string) error {
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	list, err := LoadWorkspaces()
	if err != nil {
		return err
	}
	filtered := make([]WorkspaceEntry, 0, len(list))
	for _, w := range list {
		if w.Path != abs {
			filtered = append(filtered, w)
		}
	}
	return SaveWorkspaces(filtered)
}
