package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bronya/mini-agent/internal/acl"
	"github.com/bronya/mini-agent/internal/checkpoint"
	"github.com/bronya/mini-agent/internal/config"
	cronpkg "github.com/bronya/mini-agent/internal/cron"
	"github.com/bronya/mini-agent/internal/gateway"
	"github.com/bronya/mini-agent/internal/memory"
	"github.com/bronya/mini-agent/internal/plugin"
	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/ratelimit"
	"github.com/bronya/mini-agent/internal/runner"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/skill"
	"github.com/bronya/mini-agent/internal/tool"
	"github.com/bronya/mini-agent/internal/webui"
)

const Version = "0.1.0"

const baseSystemPrompt = `You are AgentGo, a warm, careful AI coding agent running in a local CLI.

Help the user solve software engineering tasks. Prefer precise, minimal changes. Use tools when they are useful, explain blockers clearly, and be careful with destructive actions.`

type App struct {
	Config     *config.Config
	ConfigPath string // 配置文件路径，用于持久化
	Provider   provider.Provider
	Tools      *tool.Registry
	Hooks      *plugin.Hooks
	ACL        *acl.Service
	Sessions   *session.Pool
	Runner     *runner.Runner
	Cron       *cronpkg.Service
	Skills     []skill.Skill
	Checkpoint *checkpoint.Manager
	Workspace  string
	Version    string
}

type Options struct {
	ConfigPath   string
	ExecApproval runner.ExecApprovalFn
	ProviderType string // CLI override: "openai" or "anthropic"
	BaseURL      string // CLI override
	APIKey       string // CLI override
	Model        string // CLI override
}

func New(ctx context.Context, opts Options) (*App, error) {
	_ = ctx
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	var p provider.Provider
	if opts.BaseURL != "" && opts.Model != "" {
		p, err = buildProviderFromCLI(opts)
	} else {
		p, err = buildProvider(cfg)
	}
	if err != nil {
		return nil, err
	}

	workspace, err := filepath.Abs(cfg.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	cfg.WorkspaceDir = workspace

	registry := tool.NewRegistry()
	registry.RegisterAll(tool.Builtins(workspace))
	registry.RegisterAll(tool.GitTools(workspace))
	registry.Register(tool.WebhookNotify(cfg.WebhookURLs))
	if cfg.WebSearch.Enabled {
		registry.Register(tool.WebSearch(tool.WebSearchConfig{
			Engine:  cfg.WebSearch.Engine,
			APIKey:  cfg.WebSearch.APIKey,
			BaseURL: cfg.WebSearch.BaseURL,
		}))
	}

	var cronSvc *cronpkg.Service
	cronSvc = cronpkg.NewService(nil)
	registry.RegisterAll(tool.CronTools(cronSvc))

	if cfg.Memory.Enabled {
		registry.RegisterAll(memoryTools(cfg, p))
	}

	hooks := plugin.NewHooks()
	aclSvc := acl.NewService(acl.Config{
		Enabled:       cfg.ACL.Enabled,
		DefaultPolicy: cfg.ACL.DefaultPolicy,
		Admins:        cfg.ACL.Admins,
		AllowUsers:    cfg.ACL.AllowUsers,
		DenyUsers:     cfg.ACL.DenyUsers,
		DenyTools:     cfg.ACL.DenyTools,
	})

	sessions := session.NewPool(filepath.Join(workspace, ".agent", "sessions"))
	systemPrompt, allSkills := buildSystemPrompt(cfg)

	ckpt := checkpoint.New(workspace)
	// 钩子：write_file / edit_file 执行前先快照
	hooks.OnBeforeToolCall(func(ctx context.Context, name string, args tool.Args) tool.Args {
		switch name {
		case "write_file", "edit_file":
			if rel, ok := args["path"].(string); ok && rel != "" {
				_ = ckpt.SnapshotFile(rel)
			}
		}
		return args
	})

	r := runner.New(runner.Config{
		Provider:     p,
		Tools:        registry,
		Hooks:        hooks,
		ACL:          aclSvc,
		SystemPrompt: systemPrompt,
		MaxTurns:     cfg.MaxTurns,
		MaxTokens:    cfg.MaxContextTokens,
		ExecApproval: opts.ExecApproval,
		Checkpoint:   ckpt,
	})
	registry.RegisterAll(runner.SubAgentTools(r))

	cronSvc.SetCallback(func(ctx context.Context, job cronpkg.Job) error {
		sess := sessions.Get("cron-" + job.ID)
		return r.Run(ctx, sess, job.Prompt, nil)
	})
	for _, job := range cfg.Crons {
		if err := cronSvc.Add(job.ID, job.Schedule, job.Prompt); err != nil {
			return nil, fmt.Errorf("add cron %q: %w", job.ID, err)
		}
	}

	return &App{
		Config:     cfg,
		ConfigPath: opts.ConfigPath,
		Provider:   p,
		Tools:      registry,
		Hooks:      hooks,
		ACL:        aclSvc,
		Sessions:   sessions,
		Runner:     r,
		Cron:       cronSvc,
		Skills:     allSkills,
		Checkpoint: ckpt,
		Workspace:  workspace,
		Version:    Version,
	}, nil
}

func (a *App) StartCron() {
	if a.Cron != nil {
		a.Cron.Start()
	}
}

func (a *App) StopCron() {
	if a.Cron != nil {
		a.Cron.Stop()
	}
}

// RebuildProvider 根据当前 Config 中的 provider 配置重建 provider 并更新 runner。
func (a *App) RebuildProvider() error {
	a.Config.Providers = []config.ProviderConfig{a.Config.Provider}
	p, err := buildProvider(a.Config)
	if err != nil {
		return err
	}
	a.Provider = p
	a.Runner.SetProvider(p)
	return nil
}

// SaveConfig 将当前配置持久化到磁盘。
func (a *App) SaveConfig() error {
	if a.ConfigPath == "" {
		return fmt.Errorf("no config path set")
	}
	return a.Config.Save(a.ConfigPath)
}

// SetExecApproval 更新 runner 的命令审批回调。
func (a *App) SetExecApproval(fn runner.ExecApprovalFn) {
	if a.Runner != nil {
		a.Runner.SetExecApproval(fn)
	}
}

func (a *App) NewGateway() *gateway.Server {
	limiter := ratelimit.New(ratelimit.Config{
		Enabled:        a.Config.RateLimit.Enabled,
		RequestsPerSec: a.Config.RateLimit.RequestsPerSec,
		Burst:          a.Config.RateLimit.Burst,
		TokenQuota:     a.Config.RateLimit.TokenQuota,
	})
	g := gateway.New(a.Runner, a.Sessions, a.Config.Gateway.Addr, a.Config.Gateway.Token, limiter)
	webui.New(a.Sessions, a.Tools, a.Version).RegisterRoutes(g.Mux())
	return g
}

func buildProvider(cfg *config.Config) (provider.Provider, error) {
	if len(cfg.Providers) == 0 {
		return nil, nil
	}

	providers := make([]provider.Provider, 0, len(cfg.Providers))
	tiers := provider.RouterConfig{}
	hasTier := false

	for i, pc := range cfg.Providers {
		pType := strings.ToLower(pc.Type)
		if pType == "" {
			pType = "openai"
		}
		if pType != "openai" && pType != "anthropic" {
			return nil, fmt.Errorf("unsupported provider type %q (use openai or anthropic)", pc.Type)
		}
		id := pc.ID
		if id == "" {
			id = fmt.Sprintf("provider-%d", i+1)
		}
		if pc.Model == "" {
			return nil, fmt.Errorf("provider %q missing model", id)
		}
		p := buildSingleProvider(pType, id, pc.BaseURL, pc.APIKey, pc.Model, pc.Timeout)
		providers = append(providers, p)

		switch provider.Tier(strings.ToLower(pc.Tier)) {
		case provider.TierFast:
			tiers.Fast = p
			hasTier = true
		case provider.TierBalanced:
			tiers.Balanced = p
			hasTier = true
		case provider.TierPowerful:
			tiers.Powerful = p
			hasTier = true
		}
	}

	if hasTier {
		if tiers.Balanced == nil {
			tiers.Balanced = providers[0]
		}
		return provider.NewRouter(tiers), nil
	}
	if len(providers) == 1 {
		return providers[0], nil
	}
	return provider.NewFailover(providers...), nil
}

// buildSingleProvider 根据类型创建单个 provider 实例。
func buildSingleProvider(pType, id, baseURL, apiKey, model string, timeout time.Duration) provider.Provider {
	switch pType {
	case "anthropic":
		return provider.NewAnthropic(id, baseURL, apiKey, model, timeout)
	default:
		return provider.NewOpenAI(id, baseURL, apiKey, model, timeout)
	}
}

func buildProviderFromCLI(opts Options) (provider.Provider, error) {
	pType := strings.ToLower(opts.ProviderType)
	if pType == "" {
		pType = "openai"
	}
	switch pType {
	case "openai", "anthropic":
		return buildSingleProvider(pType, "cli", opts.BaseURL, opts.APIKey, opts.Model, 120*time.Second), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q (use openai or anthropic)", pType)
	}
}

func buildSystemPrompt(cfg *config.Config) (string, []skill.Skill) {
	var sections []string
	allSkills := skill.LoadAll(cfg.SkillDirs)
	if section := skill.BuildPromptSection(allSkills, 24_000); section != "" {
		sections = append(sections, section)
	}
	if bootstrap := runner.LoadBootstrapFiles(cfg.WorkspaceDir, 8000, 20000); bootstrap != "" {
		sections = append(sections, bootstrap)
	}
	if cfg.SystemPromptExtra != "" {
		sections = append(sections, cfg.SystemPromptExtra)
	}
	return runner.BuildSystemPrompt(baseSystemPrompt, strings.Join(sections, "\n\n")), allSkills
}

// ContextBreakdown 返回系统提示词各部分的 token 估算。
type ContextBreakdown struct {
	BasePrompt    int
	Skills        int
	Bootstrap     int
	Extra         int
	Total         int
	SkillNames    []string
	BootstrapFile []string
}

// GetContextBreakdown 返回当前系统提示词的结构分析。
func (a *App) GetContextBreakdown() ContextBreakdown {
	cfg := a.Config
	bd := ContextBreakdown{}

	bd.BasePrompt = estimateTokensFromChars(len(baseSystemPrompt))

	allSkills := skill.LoadAll(cfg.SkillDirs)
	skillSection := skill.BuildPromptSection(allSkills, 24_000)
	bd.Skills = estimateTokensFromChars(len(skillSection))
	for _, s := range allSkills {
		if s.CheckEligibility() == nil {
			bd.SkillNames = append(bd.SkillNames, s.Metadata.Name)
		}
	}

	bootstrap := runner.LoadBootstrapFiles(cfg.WorkspaceDir, 8000, 20000)
	bd.Bootstrap = estimateTokensFromChars(len(bootstrap))
	// 检测哪些 bootstrap 文件被加载了
	for _, name := range []string{"AGENTS.md", "AGENT.md", "CLAUDE.md", ".agent/bootstrap.md", ".agent/instructions.md", "INSTRUCTIONS.md"} {
		fp := filepath.Join(cfg.WorkspaceDir, name)
		if _, err := os.Stat(fp); err == nil {
			bd.BootstrapFile = append(bd.BootstrapFile, name)
		}
	}

	if cfg.SystemPromptExtra != "" {
		bd.Extra = estimateTokensFromChars(len(cfg.SystemPromptExtra))
	}

	bd.Total = bd.BasePrompt + bd.Skills + bd.Bootstrap + bd.Extra
	return bd
}

func estimateTokensFromChars(chars int) int {
	return (chars + 3) / 4
}

func memoryTools(cfg *config.Config, p provider.Provider) []tool.Tool {
	dir := cfg.Memory.Dir
	if dir == "" {
		dir = filepath.Join(cfg.WorkspaceDir, ".agent", "memory")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cfg.WorkspaceDir, dir)
	}

	var embedder memory.Embedder
	if cfg.Memory.EmbeddingModel != "" {
		baseURL := cfg.Memory.EmbeddingURL
		apiKey := cfg.Memory.EmbeddingAPIKey
		if baseURL == "" || apiKey == "" {
			for _, pc := range cfg.Providers {
				if baseURL == "" {
					baseURL = pc.BaseURL
				}
				if apiKey == "" {
					apiKey = pc.APIKey
				}
				break
			}
		}
		embedder = memory.NewOpenAIEmbedder(strings.TrimRight(baseURL, "/"), apiKey, cfg.Memory.EmbeddingModel)
	}

	store := memory.NewVectorStore(dir, embedder)
	_ = p
	return []tool.Tool{
		{
			Name:        "memory_add",
			Description: "Save a durable memory for future conversations. Use for stable user preferences, project context, or useful facts.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{"type": "string", "description": "Memory content to save"},
					"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"content"},
			},
			Execute: func(ctx context.Context, args tool.Args) tool.Result {
				content, err := tool.MustGetString(args, "content")
				if err != nil {
					return tool.Errf("%v", err)
				}
				var tags []string
				if raw, ok := args["tags"].([]any); ok {
					for _, item := range raw {
						if s, ok := item.(string); ok {
							tags = append(tags, s)
						}
					}
				}
				id := store.Add(ctx, content, tags...)
				return tool.OK("memory saved: " + id)
			},
		},
		{
			Name:        "memory_search",
			Description: "Search durable memories by keyword or semantic similarity.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "Search query"},
					"max_results": map[string]any{"type": "number", "description": "Maximum results, default 6"},
				},
				"required": []string{"query"},
			},
			Execute: func(ctx context.Context, args tool.Args) tool.Result {
				query, err := tool.MustGetString(args, "query")
				if err != nil {
					return tool.Errf("%v", err)
				}
				maxResults := 6
				if v, ok := args["max_results"].(float64); ok && v > 0 {
					maxResults = int(v)
				}
				entries := store.SemanticSearch(ctx, query, maxResults)
				if len(entries) == 0 {
					return tool.OK("No memories found.")
				}
				var sb strings.Builder
				for _, e := range entries {
					fmt.Fprintf(&sb, "- [%s] %s", e.ID, e.Content)
					if len(e.Tags) > 0 {
						fmt.Fprintf(&sb, " tags=%v", e.Tags)
					}
					sb.WriteString("\n")
				}
				return tool.OK(sb.String())
			},
		},
	}
}
