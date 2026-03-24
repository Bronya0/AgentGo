// Package runner — Bootstrap 文件注入。
//
// 自动扫描工作区根目录的 bootstrap 文件（如 AGENT.md、CLAUDE.md、.agent/bootstrap.md），
// 将其内容注入 system prompt，让 agent 了解项目上下文。
//
// 参考 OpenClaw 的 bootstrap-files.ts 设计：
//   - 按优先级发现文件
//   - 单个文件内容截断保护
//   - 总预算保护（防止 bootstrap 过大撑爆 context）
package runner

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// bootstrapFiles 是按优先级排列的候选 bootstrap 文件名。
var bootstrapFiles = []string{
	"AGENT.md",
	"CLAUDE.md",
	".agent/bootstrap.md",
	".agent/instructions.md",
	"INSTRUCTIONS.md",
}

// LoadBootstrapFiles 从工作区目录中扫描 bootstrap 文件并返回合并的 prompt 区块。
// maxFileChars 限制单个文件最大字符数（默认 8000）。
// maxTotalChars 限制所有 bootstrap 文件总字符数（默认 20000）。
func LoadBootstrapFiles(workspaceDir string, maxFileChars, maxTotalChars int) string {
	if workspaceDir == "" {
		return ""
	}
	if maxFileChars <= 0 {
		maxFileChars = 8000
	}
	if maxTotalChars <= 0 {
		maxTotalChars = 20000
	}

	var sections []string
	totalChars := 0

	for _, name := range bootstrapFiles {
		fp := filepath.Join(workspaceDir, name)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		// 单文件截断
		truncated := false
		if len(content) > maxFileChars {
			content = content[:maxFileChars]
			truncated = true
		}

		// 总预算检查
		if totalChars+len(content) > maxTotalChars && totalChars > 0 {
			slog.Warn("bootstrap budget exceeded, skipping remaining files",
				"file", name, "total_so_far", totalChars)
			break
		}

		section := fmt.Sprintf("### %s\n%s", name, content)
		if truncated {
			section += "\n...[truncated — file exceeds budget]"
		}
		sections = append(sections, section)
		totalChars += len(content)

		slog.Debug("bootstrap file loaded", "file", name, "chars", len(content), "truncated", truncated)
	}

	if len(sections) == 0 {
		return ""
	}

	return "## Project Bootstrap\n\nThe following project context files were found in the workspace:\n\n" +
		strings.Join(sections, "\n\n")
}
