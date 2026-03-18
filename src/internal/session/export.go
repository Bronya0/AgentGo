// Package session — 对话历史导出/导入功能。
//
// 支持两种格式：
//   - JSON: 完整结构化数据（可精确还原）
//   - Markdown: 人类可读格式
package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bronya/mini-agent/internal/provider"
)

// ExportFormat 导出格式。
type ExportFormat string

const (
	FormatJSON     ExportFormat = "json"
	FormatMarkdown ExportFormat = "markdown"
)

// ExportData 是导出的完整数据结构（JSON 格式用）。
type ExportData struct {
	Version   int                `json:"version"`
	SessionID string             `json:"session_id"`
	ExportAt  string             `json:"export_at"`
	Summary   string             `json:"summary,omitempty"`
	Messages  []provider.Message `json:"messages"`
}

// Export 将会话导出为指定格式的字符串。
func (s *Session) Export(format ExportFormat) (string, error) {
	s.mu.Lock()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	summary := s.Summary
	sid := s.ID
	s.mu.Unlock()

	switch format {
	case FormatJSON:
		return exportJSON(sid, summary, msgs)
	case FormatMarkdown:
		return exportMarkdown(sid, summary, msgs), nil
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}
}

func exportJSON(sessionID, summary string, msgs []provider.Message) (string, error) {
	data := ExportData{
		Version:   1,
		SessionID: sessionID,
		ExportAt:  time.Now().Format(time.RFC3339),
		Summary:   summary,
		Messages:  msgs,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func exportMarkdown(sessionID, summary string, msgs []provider.Message) string {
	var sb strings.Builder
	sb.WriteString("# Conversation: ")
	sb.WriteString(sessionID)
	sb.WriteString("\n\n")
	sb.WriteString("> Exported at ")
	sb.WriteString(time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString("\n\n")

	if summary != "" {
		sb.WriteString("## Context Summary\n\n")
		sb.WriteString(summary)
		sb.WriteString("\n\n---\n\n")
	}

	for _, m := range msgs {
		switch m.Role {
		case provider.RoleSystem:
			sb.WriteString("### 🔧 System\n\n")
			sb.WriteString(m.Content)
		case provider.RoleUser:
			sb.WriteString("### 👤 User\n\n")
			sb.WriteString(m.Content)
		case provider.RoleAssistant:
			sb.WriteString("### 🤖 Assistant\n\n")
			if m.Content != "" {
				sb.WriteString(m.Content)
			}
			if len(m.ToolCalls) > 0 {
				sb.WriteString("\n\n**Tool Calls:**\n")
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&sb, "- `%s`(%s)\n", tc.Name, tc.Arguments)
				}
			}
		case provider.RoleTool:
			sb.WriteString("### 🔨 Tool Result")
			if m.ToolCallID != "" {
				fmt.Fprintf(&sb, " (id: %s)", m.ToolCallID)
			}
			sb.WriteString("\n\n```\n")
			content := m.Content
			if len(content) > 2000 {
				content = content[:2000] + "\n...[truncated]"
			}
			sb.WriteString(content)
			sb.WriteString("\n```")
		}
		sb.WriteString("\n\n---\n\n")
	}

	return sb.String()
}

// Import 从 JSON 数据导入会话，替换当前内容。
func (s *Session) Import(data []byte) error {
	var ed ExportData
	if err := json.Unmarshal(data, &ed); err != nil {
		return fmt.Errorf("invalid import data: %w", err)
	}
	if ed.Version == 0 {
		return fmt.Errorf("invalid import data: missing version field")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = ed.Messages
	s.Summary = ed.Summary
	return nil
}
