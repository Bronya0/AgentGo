// Package tool — 定时任务管理工具：通过对话动态创建/删除/列出定时任务。
package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/bronya/mini-agent/internal/cron"
)

// CronTools 返回定时任务管理工具。
func CronTools(cronSvc *cron.Service) []Tool {
	return []Tool{
		CronAdd(cronSvc),
		CronList(cronSvc),
		CronRemove(cronSvc),
	}
}

// CronAdd 动态添加定时任务。
func CronAdd(cronSvc *cron.Service) Tool {
	return Tool{
		Name:        "cron_add",
		Description: "Add a scheduled task that runs periodically. Supports 'every Nm/Nh/Ns' for intervals or 'daily HH:MM' for daily scheduling. The task prompt will be sent to the agent at the scheduled time.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Unique ID for the cron job (e.g. 'daily-review')",
				},
				"schedule": map[string]any{
					"type":        "string",
					"description": "Schedule expression: 'every 5m', 'every 1h', 'daily 19:00', etc.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The prompt to execute when the job triggers (describe the task for the agent to perform)",
				},
			},
			"required": []string{"id", "schedule", "prompt"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			id, err := MustGetString(args, "id")
			if err != nil {
				return Errf("%v", err)
			}
			schedule, err := MustGetString(args, "schedule")
			if err != nil {
				return Errf("%v", err)
			}
			prompt, err := MustGetString(args, "prompt")
			if err != nil {
				return Errf("%v", err)
			}

			if err := cronSvc.Add(id, schedule, prompt); err != nil {
				return Errf("添加定时任务失败: %v", err)
			}

			return OK(fmt.Sprintf("定时任务 %q 已创建（调度: %s）", id, schedule))
		},
	}
}

// CronList 列出所有定时任务。
func CronList(cronSvc *cron.Service) Tool {
	return Tool{
		Name:        "cron_list",
		Description: "List all currently registered scheduled tasks.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(ctx context.Context, args Args) Result {
			jobs := cronSvc.List()
			if len(jobs) == 0 {
				return OK("当前没有定时任务。")
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("共 %d 个定时任务：\n\n", len(jobs)))
			for _, j := range jobs {
				sb.WriteString(fmt.Sprintf("- **%s** (调度: %s)\n  任务: %s\n\n",
					j.ID, j.Schedule, truncatePrompt(j.Prompt, 100)))
			}
			return OK(sb.String())
		},
	}
}

// CronRemove 删除定时任务。
func CronRemove(cronSvc *cron.Service) Tool {
	return Tool{
		Name:        "cron_remove",
		Description: "Remove a scheduled task by its ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The ID of the cron job to remove",
				},
			},
			"required": []string{"id"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			id, err := MustGetString(args, "id")
			if err != nil {
				return Errf("%v", err)
			}

			if cronSvc.Remove(id) {
				return OK(fmt.Sprintf("定时任务 %q 已删除", id))
			}
			return Errf("未找到定时任务 %q", id)
		},
	}
}

func truncatePrompt(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
