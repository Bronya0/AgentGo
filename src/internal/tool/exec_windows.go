//go:build windows

package tool

import (
	"context"
	"os/exec"
)

// buildShellCommand 创建 Windows 平台的 shell 命令（使用 cmd.exe）。
func buildShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", command)
}

// setProcAttr Windows 平台无需进程组隔离，CommandContext 默认会终止进程。
func setProcAttr(cmd *exec.Cmd) {
	// Windows 上 exec.CommandContext 在超时时会自动终止进程
}
