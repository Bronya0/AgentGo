package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/bronya/mini-agent/internal/app"
	"github.com/bronya/mini-agent/internal/cli"
	"github.com/bronya/mini-agent/internal/cli/tui"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	chat := flag.String("chat", "", "send one prompt and exit")
	sessionID := flag.String("session", "default", "session id")
	server := flag.Bool("server", false, "start HTTP gateway and Web UI")
	verbose := flag.Bool("v", false, "enable debug logging")

	providerType := flag.String("provider-type", "", "provider protocol: openai (default) or anthropic")
	baseURL := flag.String("base-url", "", "LLM API base URL")
	apiKey := flag.String("api-key", "", "LLM API key")
	model := flag.String("model", "", "model name")
	flag.Parse()

	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// TUI 模式下，日志写到 stderr 会搞乱界面 — 禁用结构化日志或写到文件
	// 对于 -chat / -server 模式正常输出
	if *chat == "" && !*server {
		// TUI 模式：日志重定向到 .agent/tui.log
		_ = os.MkdirAll(".agent", 0o755)
		if f, err := os.OpenFile(".agent/tui.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
		} else {
			// 无法写日志文件，丢弃
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{
		ConfigPath:   *configPath,
		ExecApproval: nil, // TUI 模式稍后通过 SetExecApproval 注入
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

	if *server {
		g := a.NewGateway()
		go func() {
			<-ctx.Done()
			_ = g.Shutdown()
		}()
		fmt.Printf("AgentGo server listening on %s\n", a.Config.Gateway.Addr)
		if err := g.Start(); err != nil && err != http.ErrServerClosed {
			fatal(err)
		}
		return
	}

	if *chat != "" {
		r := cli.NewRenderer(os.Stdout)
		sess := a.Sessions.Get(*sessionID)
		if err := a.Runner.Run(ctx, sess, *chat, r.Handle); err != nil {
			fatal(err)
		}
		r.RenderFinal()
		if err := sess.Save(); err != nil {
			fatal(err)
		}
		return
	}

	// 交互模式：启动 TUI
	if err := tui.Run(ctx, a, *sessionID); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agent:", err)
	os.Exit(1)
}
