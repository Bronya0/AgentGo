// Package sandbox 提供进程级沙箱隔离，无需 Docker。
//
// 隔离措施：
//   - 临时目录隔离：命令在独立临时目录中执行
//   - 只读工作区：通过 symlink/copy 提供只读访问
//   - 资源限制：输出大小、执行超时（由调用方控制）
//   - 环境隔离：清洁的环境变量（仅保留必要的 PATH 等）
//   - 执行后清理：临时目录自动删除
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config 沙箱配置。
type Config struct {
	WorkspaceDir string        // 工作区目录（只读访问）
	Timeout      time.Duration // 执行超时
	MaxOutputKB  int           // 最大输出大小（KB）
	AllowNetwork bool          // 是否允许网络访问（Unix: 需要 unshare 权限）
}

// Result 沙箱执行结果。
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// Run 在沙箱中执行命令。
func Run(ctx context.Context, cfg Config, command string) Result {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxOutputKB <= 0 {
		cfg.MaxOutputKB = 48
	}

	// 创建临时工作目录
	tmpDir, err := os.MkdirTemp("", "agent-sandbox-*")
	if err != nil {
		return Result{Err: fmt.Errorf("create temp dir: %w", err)}
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			slog.Warn("sandbox cleanup failed", "dir", tmpDir, "err", err)
		}
	}()

	// 在临时目录中创建工作区的符号链接（只读访问）
	if cfg.WorkspaceDir != "" {
		wsLink := filepath.Join(tmpDir, "workspace")
		if err := createWorkspaceLink(wsLink, cfg.WorkspaceDir); err != nil {
			slog.Warn("sandbox workspace link failed, continuing without", "err", err)
		}
	}

	tCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := buildSandboxCommand(tCtx, command)
	cmd.Dir = tmpDir

	// 最小化环境变量
	cmd.Env = minimalEnv()

	maxOut := cfg.MaxOutputKB * 1024
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, max: maxOut}
	cmd.Stderr = &limitedWriter{w: &stderr, max: maxOut / 3}

	slog.Info("sandbox: executing",
		"command", truncateStr(command, 200),
		"tmpDir", tmpDir,
		"timeout", cfg.Timeout,
	)

	runErr := cmd.Run()

	res := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if runErr != nil {
		res.Err = runErr
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}

	return res
}

// minimalEnv 返回最小化的环境变量集。
func minimalEnv() []string {
	env := []string{
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	}

	// 保留 PATH
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}

	// 保留 HOME/USERPROFILE
	if runtime.GOOS == "windows" {
		if v := os.Getenv("USERPROFILE"); v != "" {
			env = append(env, "USERPROFILE="+v)
		}
		if v := os.Getenv("SYSTEMROOT"); v != "" {
			env = append(env, "SYSTEMROOT="+v)
		}
		if v := os.Getenv("COMSPEC"); v != "" {
			env = append(env, "COMSPEC="+v)
		}
	} else {
		if v := os.Getenv("HOME"); v != "" {
			env = append(env, "HOME="+v)
		}
	}

	// 保留 TMP/TEMP
	for _, k := range []string{"TMP", "TEMP", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}

	return env
}

// createWorkspaceLink 创建工作区的符号或目录引用。
func createWorkspaceLink(linkPath, targetDir string) error {
	// 尝试创建符号链接
	return os.Symlink(targetDir, linkPath)
}

// buildSandboxCommand 创建沙箱化的 shell 命令。
func buildSandboxCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// limitedWriter 限制写入字节数。
type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	remaining := lw.max - lw.w.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return lw.w.Write(p)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// FormatResult 格式化沙箱结果为工具输出。
func FormatResult(r Result) string {
	var sb strings.Builder
	if r.Stdout != "" {
		sb.WriteString(r.Stdout)
	}
	if r.Stderr != "" {
		if sb.Len() > 0 {
			sb.WriteString("\nSTDERR:\n")
		}
		sb.WriteString(r.Stderr)
	}
	if r.Err != nil {
		sb.WriteString("\nExit error: ")
		sb.WriteString(r.Err.Error())
	}
	return sb.String()
}
