//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"syscall"
)

// buildShellCommand 创建平台相应的 shell 命令。
func buildShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// setProcAttr 设置进程组隔离，超时时终止所有子进程。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
