// Package runner — 子 Agent 委托工具。
//
// 允许 Agent 将复杂子任务委托给独立的子 Agent 处理。
// 每个子 Agent 拥有独立的 session、可选的独立工具集和 system prompt。
// 支持并行执行多个子任务并汇总结果。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
)

// SubAgentTools 返回子 Agent 委托相关工具。
// runner 参数用于创建子 Agent 会话。
func SubAgentTools(r *Runner) []tool.Tool {
	return []tool.Tool{
		delegateSubAgent(r),
		parallelSubAgents(r),
	}
}

// delegateSubAgent 将单个子任务委托给子 Agent 执行。
func delegateSubAgent(r *Runner) tool.Tool {
	return tool.Tool{
		Name:        "delegate_task",
		Description: "Delegate a sub-task to an independent sub-agent. The sub-agent has its own session and can use all available tools. Use this for complex tasks that benefit from focused context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Detailed description of the task for the sub-agent",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Additional context or constraints for the sub-agent",
				},
			},
			"required": []string{"task"},
		},
		Execute: func(ctx context.Context, args tool.Args) tool.Result {
			task, err := tool.MustGetString(args, "task")
			if err != nil {
				return tool.Errf("%v", err)
			}
			extra, _ := args["context"].(string)

			result, runErr := runSubAgent(ctx, r, task, extra)
			if runErr != nil {
				return tool.Errf("sub-agent failed: %v", runErr)
			}
			return tool.OK(result)
		},
	}
}

// parallelSubAgents 并行执行多个子任务。
func parallelSubAgents(r *Runner) tool.Tool {
	return tool.Tool{
		Name:        "parallel_tasks",
		Description: "Execute multiple sub-tasks in parallel using independent sub-agents. Each task gets its own session. Returns combined results.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "Array of task descriptions to execute in parallel",
					"items": map[string]any{
						"type":        "string",
						"description": "Task description",
					},
				},
			},
			"required": []string{"tasks"},
		},
		Execute: func(ctx context.Context, args tool.Args) tool.Result {
			tasksRaw, ok := args["tasks"]
			if !ok {
				return tool.Errf("tasks is required")
			}

			// 解析 tasks 数组
			var tasks []string
			switch v := tasksRaw.(type) {
			case []any:
				for _, item := range v {
					if s, ok := item.(string); ok {
						tasks = append(tasks, s)
					}
				}
			case string:
				// 如果传入的是 JSON 字符串
				if err := json.Unmarshal([]byte(v), &tasks); err != nil {
					return tool.Errf("invalid tasks format: %v", err)
				}
			default:
				return tool.Errf("tasks must be an array of strings")
			}

			if len(tasks) == 0 {
				return tool.Errf("no tasks provided")
			}
			if len(tasks) > 5 {
				return tool.Errf("maximum 5 parallel tasks allowed")
			}

			type taskResult struct {
				idx    int
				result string
				err    error
			}

			results := make([]taskResult, len(tasks))
			var wg sync.WaitGroup

			for i, task := range tasks {
				wg.Add(1)
				go func(idx int, t string) {
					defer wg.Done()
					res, err := runSubAgent(ctx, r, t, "")
					results[idx] = taskResult{idx: idx, result: res, err: err}
				}(i, task)
			}
			wg.Wait()

			var sb strings.Builder
			for i, res := range results {
				fmt.Fprintf(&sb, "## Task %d: %s\n\n", i+1, truncateTask(tasks[i], 80))
				if res.err != nil {
					fmt.Fprintf(&sb, "**Error:** %v\n\n", res.err)
				} else {
					sb.WriteString(res.result)
					sb.WriteString("\n\n")
				}
				sb.WriteString("---\n\n")
			}

			return tool.OK(sb.String())
		},
	}
}

// runSubAgent 运行一个子 Agent 并返回最终文本回复。
func runSubAgent(ctx context.Context, r *Runner, task string, extra string) (string, error) {
	// 创建独立 session
	subSess := session.New("sub-agent")

	// 构建任务 prompt
	prompt := task
	if extra != "" {
		prompt = task + "\n\nAdditional context: " + extra
	}

	slog.Info("sub-agent started", "task", truncateTask(task, 100))

	var result strings.Builder

	err := r.Run(ctx, subSess, prompt, func(chunk StreamChunk) {
		if chunk.Event == EventText {
			result.WriteString(chunk.Text)
		}
	})

	if err != nil {
		return "", err
	}

	reply := result.String()
	if reply == "" {
		// 从 session 中提取最后一条 assistant 消息
		history := subSess.History()
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == provider.RoleAssistant && history[i].Content != "" {
				reply = history[i].Content
				break
			}
		}
	}

	slog.Info("sub-agent completed", "task", truncateTask(task, 100), "reply_len", len(reply))
	return reply, nil
}

func truncateTask(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
