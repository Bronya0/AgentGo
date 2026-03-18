// Package tool — Git 相关工具：拉取代码、查看日志、查看 diff。
package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitTools 返回 Git 相关的工具列表。
func GitTools(workspaceDir string) []Tool {
	return []Tool{
		GitPull(workspaceDir),
		GitLog(workspaceDir),
		GitDiff(workspaceDir),
		GitShow(workspaceDir),
	}
}

// GitPull 拉取远程仓库最新代码。
func GitPull(workspaceDir string) Tool {
	return Tool{
		Name:        "git_pull",
		Description: "Pull latest changes from the remote repository. Optionally specify a branch.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository (relative to workspace root). Defaults to workspace root.",
				},
				"branch": map[string]any{
					"type":        "string",
					"description": "Branch to pull (default: current branch)",
				},
			},
		},
		Execute: func(ctx context.Context, args Args) Result {
			repoPath := workspaceDir
			if p, ok := args["repo_path"].(string); ok && p != "" {
				abs, err := safeJoin(workspaceDir, p)
				if err != nil {
					return Errf("path error: %v", err)
				}
				repoPath = abs
			}

			gitArgs := []string{"pull", "--ff-only"}
			if branch, ok := args["branch"].(string); ok && branch != "" {
				gitArgs = append(gitArgs, "origin", branch)
			}

			return runGit(ctx, repoPath, gitArgs, 60*time.Second)
		},
	}
}

// GitLog 查看 Git 提交日志。
func GitLog(workspaceDir string) Tool {
	return Tool{
		Name:        "git_log",
		Description: "Show git commit log. Can filter by date range, author, or number of commits.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository (relative to workspace root)",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "Show commits after this date, e.g. '1 day ago', '2024-01-01', 'yesterday'",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "Show commits before this date",
				},
				"author": map[string]any{
					"type":        "string",
					"description": "Filter by author name or email",
				},
				"max_count": map[string]any{
					"type":        "number",
					"description": "Maximum number of commits to show (default: 20)",
				},
			},
		},
		Execute: func(ctx context.Context, args Args) Result {
			repoPath := workspaceDir
			if p, ok := args["repo_path"].(string); ok && p != "" {
				abs, err := safeJoin(workspaceDir, p)
				if err != nil {
					return Errf("path error: %v", err)
				}
				repoPath = abs
			}

			gitArgs := []string{"log", "--oneline", "--no-merges", "--stat"}

			if since, ok := args["since"].(string); ok && since != "" {
				gitArgs = append(gitArgs, "--since="+since)
			}
			if until, ok := args["until"].(string); ok && until != "" {
				gitArgs = append(gitArgs, "--until="+until)
			}
			if author, ok := args["author"].(string); ok && author != "" {
				gitArgs = append(gitArgs, "--author="+author)
			}

			maxCount := 20
			if v, ok := args["max_count"].(float64); ok && v > 0 {
				maxCount = int(v)
				if maxCount > 100 {
					maxCount = 100
				}
			}
			gitArgs = append(gitArgs, fmt.Sprintf("-n%d", maxCount))

			return runGit(ctx, repoPath, gitArgs, 30*time.Second)
		},
	}
}

// GitDiff 查看代码变更 diff。
func GitDiff(workspaceDir string) Tool {
	return Tool{
		Name:        "git_diff",
		Description: "Show code changes (diff). Can compare branches, commits, or show today's changes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository (relative to workspace root)",
				},
				"ref": map[string]any{
					"type":        "string",
					"description": "Git ref to diff against, e.g. 'HEAD~5', 'main', a commit hash, or 'HEAD@{1 day ago}'",
				},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Only show diff for a specific file or directory",
				},
				"stat_only": map[string]any{
					"type":        "boolean",
					"description": "If true, only show file change statistics (no detailed diff)",
				},
			},
		},
		Execute: func(ctx context.Context, args Args) Result {
			repoPath := workspaceDir
			if p, ok := args["repo_path"].(string); ok && p != "" {
				abs, err := safeJoin(workspaceDir, p)
				if err != nil {
					return Errf("path error: %v", err)
				}
				repoPath = abs
			}

			gitArgs := []string{"diff"}

			if ref, ok := args["ref"].(string); ok && ref != "" {
				gitArgs = append(gitArgs, ref)
			}

			if statOnly, ok := args["stat_only"].(bool); ok && statOnly {
				gitArgs = append(gitArgs, "--stat")
			}

			if filePath, ok := args["file_path"].(string); ok && filePath != "" {
				gitArgs = append(gitArgs, "--", filePath)
			}

			return runGit(ctx, repoPath, gitArgs, 30*time.Second)
		},
	}
}

// GitShow 查看指定 commit 的详细信息和 diff。
func GitShow(workspaceDir string) Tool {
	return Tool{
		Name:        "git_show",
		Description: "Show detailed information and diff for a specific commit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{
					"type":        "string",
					"description": "Path to the git repository (relative to workspace root)",
				},
				"commit": map[string]any{
					"type":        "string",
					"description": "Commit hash or reference (default: HEAD)",
				},
			},
		},
		Execute: func(ctx context.Context, args Args) Result {
			repoPath := workspaceDir
			if p, ok := args["repo_path"].(string); ok && p != "" {
				abs, err := safeJoin(workspaceDir, p)
				if err != nil {
					return Errf("path error: %v", err)
				}
				repoPath = abs
			}

			commit := "HEAD"
			if c, ok := args["commit"].(string); ok && c != "" {
				commit = c
			}

			gitArgs := []string{"show", "--stat", "--patch", commit}
			return runGit(ctx, repoPath, gitArgs, 30*time.Second)
		},
	}
}

// runGit 执行 git 命令并返回结果。
func runGit(ctx context.Context, repoPath string, gitArgs []string, timeout time.Duration) Result {
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(tCtx, "git", gitArgs...)
	cmd.Dir = repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var sb strings.Builder
	if stdout.Len() > 0 {
		sb.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\nSTDERR:\n")
		}
		sb.WriteString(stderr.String())
	}

	output := sb.String()
	const maxOutput = 48 * 1024 // git diff 可能很长
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n...[truncated]"
	}

	if output == "" && err == nil {
		return OK("(no output)")
	}

	if err != nil {
		return Result{Content: output + "\nGit error: " + err.Error(), IsError: true}
	}
	return OK(output)
}
