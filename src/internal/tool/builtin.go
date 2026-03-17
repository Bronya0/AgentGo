// Package tool — 内置工具实现：文件读写编辑、搜索文件、执行命令、Web Fetch、目录列表。
package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Builtins 返回所有内置工具的列表。
// workspaceDir 是文件操作的根目录（安全沙箱）。
func Builtins(workspaceDir string) []Tool {
	return []Tool{
		ReadFile(workspaceDir),
		WriteFile(workspaceDir),
		EditFile(workspaceDir),
		ListDir(workspaceDir),
		GrepFiles(workspaceDir),
		RunCommand(workspaceDir),
		WebFetch(),
	}
}

// ReadFile 读取文件内容（限制在 workspaceDir 内）。
func ReadFile(workspaceDir string) Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read the contents of a file. Path must be relative to the workspace root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file from workspace root",
				},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			rel, err := MustGetString(args, "path")
			if err != nil {
				return Errf("%v", err)
			}
			abs, err := safeJoin(workspaceDir, rel)
			if err != nil {
				return Errf("path traversal not allowed: %v", err)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return Errf("read_file: %v", err)
			}
			content := string(data)
			const maxBytes = 64 * 1024
			if len(data) > maxBytes {
				content = string(data[:maxBytes]) + "\n...[truncated]"
			}
			return OK(content)
		},
	}
}

// WriteFile 将内容写入文件（限制在 workspaceDir 内）。
func WriteFile(workspaceDir string) Tool {
	return Tool{
		Name:        "write_file",
		Description: "Write content to a file, creating directories as needed. Path must be relative to workspace root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file from workspace root",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write to the file",
				},
			},
			"required": []string{"path", "content"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			rel, err := MustGetString(args, "path")
			if err != nil {
				return Errf("%v", err)
			}
			content, err := MustGetString(args, "content")
			if err != nil {
				return Errf("%v", err)
			}
			abs, err := safeJoin(workspaceDir, rel)
			if err != nil {
				return Errf("path traversal not allowed: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return Errf("write_file mkdir: %v", err)
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return Errf("write_file: %v", err)
			}
			return OK(fmt.Sprintf("Written %d bytes to %s", len(content), rel))
		},
	}
}

// EditFile 对文件做精确字符串替换（无需重写全文）。
func EditFile(workspaceDir string) Tool {
	return Tool{
		Name:        "edit_file",
		Description: "Make a targeted edit to a file by replacing an exact string match. More efficient than rewriting the entire file with write_file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file from workspace root",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact string to find and replace (must be unique in the file)",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement string",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			rel, err := MustGetString(args, "path")
			if err != nil {
				return Errf("%v", err)
			}
			oldStr, err := MustGetString(args, "old_string")
			if err != nil {
				return Errf("%v", err)
			}
			newStr, err := MustGetString(args, "new_string")
			if err != nil {
				return Errf("%v", err)
			}
			abs, err := safeJoin(workspaceDir, rel)
			if err != nil {
				return Errf("path traversal not allowed: %v", err)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return Errf("edit_file read: %v", err)
			}
			content := string(data)
			count := strings.Count(content, oldStr)
			if count == 0 {
				return Errf("edit_file: old_string not found in %s", rel)
			}
			if count > 1 {
				return Errf("edit_file: old_string matches %d times in %s (must be unique)", count, rel)
			}
			newContent := strings.Replace(content, oldStr, newStr, 1)
			if err := os.WriteFile(abs, []byte(newContent), 0o644); err != nil {
				return Errf("edit_file write: %v", err)
			}
			return OK(fmt.Sprintf("Edited %s: replaced %d chars with %d chars", rel, len(oldStr), len(newStr)))
		},
	}
}

// ListDir 列出目录内容（限制在 workspaceDir 内）。
func ListDir(workspaceDir string) Tool {
	return Tool{
		Name:        "list_dir",
		Description: "List the contents of a directory. Path must be relative to the workspace root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to the directory from workspace root (use '.' for root)",
				},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			rel, err := MustGetString(args, "path")
			if err != nil {
				return Errf("%v", err)
			}
			abs, err := safeJoin(workspaceDir, rel)
			if err != nil {
				return Errf("path traversal not allowed: %v", err)
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return Errf("list_dir: %v", err)
			}
			var sb strings.Builder
			for _, e := range entries {
				if e.IsDir() {
					sb.WriteString(e.Name() + "/\n")
				} else {
					info, _ := e.Info()
					if info != nil {
						sb.WriteString(fmt.Sprintf("%s (%d bytes)\n", e.Name(), info.Size()))
					} else {
						sb.WriteString(e.Name() + "\n")
					}
				}
			}
			return OK(sb.String())
		},
	}
}

// GrepFiles 在工作区中搜索匹配的文件内容。
func GrepFiles(workspaceDir string) Tool {
	return Tool{
		Name:        "grep_files",
		Description: "Search for a pattern in files within the workspace. Returns matching lines with file paths and line numbers.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Search pattern (substring match, case-insensitive)",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Relative directory to search in (default: workspace root)",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "File glob pattern, e.g. '*.go', '*.py' (default: all files)",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			pattern, err := MustGetString(args, "pattern")
			if err != nil {
				return Errf("%v", err)
			}
			searchDir := workspaceDir
			if rel, ok := args["path"]; ok {
				if s, ok := rel.(string); ok && s != "" {
					d, err := safeJoin(workspaceDir, s)
					if err != nil {
						return Errf("path traversal not allowed: %v", err)
					}
					searchDir = d
				}
			}
			glob := ""
			if g, ok := args["glob"]; ok {
				if s, ok := g.(string); ok {
					glob = s
				}
			}

			patternLower := strings.ToLower(pattern)
			var results strings.Builder
			matchCount := 0
			const maxMatches = 100

			_ = filepath.WalkDir(searchDir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					name := d.Name()
					if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
						return filepath.SkipDir
					}
					return nil
				}
				if matchCount >= maxMatches {
					return filepath.SkipAll
				}
				if glob != "" {
					if matched, _ := filepath.Match(glob, d.Name()); !matched {
						return nil
					}
				}
				info, err := d.Info()
				if err != nil || info.Size() > 512*1024 {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				relPath, _ := filepath.Rel(workspaceDir, path)
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if strings.Contains(strings.ToLower(line), patternLower) {
						matchCount++
						results.WriteString(fmt.Sprintf("%s:%d: %s\n", relPath, i+1, truncateLine(line, 200)))
						if matchCount >= maxMatches {
							results.WriteString(fmt.Sprintf("\n...[stopped at %d matches]\n", maxMatches))
							return filepath.SkipAll
						}
					}
				}
				return nil
			})

			if matchCount == 0 {
				return OK("No matches found.")
			}
			return OK(results.String())
		},
	}
}

// RunCommand 在工作目录执行 shell 命令（使用进程组隔离）。
func RunCommand(workspaceDir string) Tool {
	return Tool{
		Name:        "run_command",
		Description: "Run a shell command in the workspace directory. Returns stdout and stderr. Use with caution.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"timeout_seconds": map[string]any{
					"type":        "number",
					"description": "Timeout in seconds (default 30, max 300)",
				},
			},
			"required": []string{"command"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			command, err := MustGetString(args, "command")
			if err != nil {
				return Errf("%v", err)
			}

			timeoutSec := 30.0
			if v, ok := args["timeout_seconds"]; ok {
				if f, ok := v.(float64); ok && f > 0 && f <= 300 {
					timeoutSec = f
				}
			}

			tCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
			defer cancel()

			cmd := exec.CommandContext(tCtx, "sh", "-c", command)
			cmd.Dir = workspaceDir
			// 使用独立进程组，超时时终止所有子进程
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error {
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			runErr := cmd.Run()

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
			const maxOutput = 32 * 1024
			if len(output) > maxOutput {
				output = output[:maxOutput] + "\n...[truncated]"
			}

			if runErr != nil {
				return Result{Content: output + "\nExit error: " + runErr.Error(), IsError: true}
			}
			return OK(output)
		},
	}
}

// WebFetch 获取 URL 的文本内容（含 SSRF 防护）。
func WebFetch() Tool {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("dns resolve %q: %w", host, err)
			}
			for _, ip := range ips {
				if isPrivateIP(ip.IP) {
					return nil, fmt.Errorf("access to private/internal address %s is blocked (SSRF protection)", ip.IP)
				}
			}
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	return Tool{
		Name:        "web_fetch",
		Description: "Fetch the text content of a public URL (HTML/JSON/plain text). Private/internal network addresses are blocked.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch (http/https only)",
				},
			},
			"required": []string{"url"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			rawURL, err := MustGetString(args, "url")
			if err != nil {
				return Errf("%v", err)
			}
			parsed, err := url.Parse(rawURL)
			if err != nil {
				return Errf("invalid URL: %v", err)
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return Errf("web_fetch: only http/https URLs are allowed")
			}

			req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
			if err != nil {
				return Errf("web_fetch: create request: %v", err)
			}
			req.Header.Set("User-Agent", "mini-agent/1.0")

			resp, err := httpClient.Do(req)
			if err != nil {
				return Errf("web_fetch: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 400 {
				return Errf("web_fetch: HTTP %d", resp.StatusCode)
			}

			const maxBody = 128 * 1024
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			if err != nil {
				return Errf("web_fetch: read body: %v", err)
			}
			return OK(string(body))
		},
	}
}

// isPrivateIP 检查是否为私有/内网 IP（防 SSRF）。
func isPrivateIP(ip net.IP) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"0.0.0.0/8",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidrStr := range privateRanges {
		_, cidr, _ := net.ParseCIDR(cidrStr)
		if cidr != nil && cidr.Contains(ip) {
			return true
		}
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// safeJoin 将 base + rel 安全组合，阻止路径穿越和 symlink 逃逸。
func safeJoin(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("workspace dir not configured")
	}
	abs := filepath.Join(base, rel)

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absClean, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absClean, baseAbs+string(filepath.Separator)) && absClean != baseAbs {
		return "", fmt.Errorf("path %q is outside workspace %q", rel, base)
	}

	// 对已存在的路径，额外检查 symlink 解析后的真实路径
	if realPath, err := filepath.EvalSymlinks(absClean); err == nil {
		realBase, err2 := filepath.EvalSymlinks(baseAbs)
		if err2 != nil {
			realBase = baseAbs
		}
		if !strings.HasPrefix(realPath, realBase+string(filepath.Separator)) && realPath != realBase {
			return "", fmt.Errorf("path %q resolves to %q which is outside workspace", rel, realPath)
		}
	}

	return absClean, nil
}

// truncateLine 截断过长的行用于搜索结果展示。
func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "..."
}

// parseInt64 安全地从 Args 获取整数。
func parseInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}
