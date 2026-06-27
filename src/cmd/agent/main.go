package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/bronya/mini-agent/internal/app"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	verbose := flag.Bool("v", false, "enable debug logging")

	providerType := flag.String("provider-type", "", "provider protocol: openai (default) or anthropic")
	baseURL := flag.String("base-url", "", "LLM API base URL")
	apiKey := flag.String("api-key", "", "LLM API key")
	model := flag.String("model", "", "model name")
	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{
		ConfigPath:   *configPath,
		ExecApproval: nil,
		ProviderType: *providerType,
		BaseURL:      *baseURL,
		APIKey:       *apiKey,
		Model:        *model,
	})
	if err != nil {
		fatal(err)
	}
	a.StartCron()
	defer a.StopCron()

	// 默认启动 HTTP 服务（Web + WebSocket API）
	// TUI 交互模式已弃用，代码保留在 internal/cli/tui/ 作为参考
	g := a.NewGateway()
	go func() {
		<-ctx.Done()
		_ = g.Shutdown()
	}()
	addr := a.Config.Gateway.Addr
	fmt.Printf("AgentGo server listening on %s\n", addr)
	go openWebUI(addr)
	if err := g.Start(); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agent:", err)
	os.Exit(1)
}

// openWebUI 在默认浏览器中打开 Web UI。
func openWebUI(addr string) {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	url := "http://" + host + "/ui"

	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default: // linux
		cmd = "xdg-open"
		args = []string{url}
	}

	if err := exec.Command(cmd, args...).Start(); err != nil {
		slog.Debug("failed to open browser", "error", err)
	}
}
