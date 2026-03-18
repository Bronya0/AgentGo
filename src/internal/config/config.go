// Package config 定义 agent 的全量配置结构。
// 配置文件为 YAML 格式（默认 config.yaml）。
// 支持环境变量展开：${ENV_VAR} 或 $ENV_VAR 语法。
package config

import (
	"log/slog"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 mini-agent 的顶层配置。
type Config struct {
	Gateway           GatewayConfig    `yaml:"gateway"`
	Provider          ProviderConfig   `yaml:"provider"`   // 单 provider 简写
	Providers         []ProviderConfig `yaml:"providers"`  // 多 provider failover 列表
	SkillDirs         []string         `yaml:"skill_dirs"`
	WorkspaceDir      string           `yaml:"workspace_dir"`
	MaxContextTokens  int              `yaml:"max_context_tokens"`
	SystemPromptExtra string           `yaml:"system_prompt_extra"`
	Crons             []CronJobConfig  `yaml:"crons"`
	Memory            MemoryConfig     `yaml:"memory"`
	Channels          ChannelsConfig   `yaml:"channels"`   // 聊天渠道配置
	ACL               ACLConfig        `yaml:"acl"`        // 访问控制配置
	MCP               MCPConfig        `yaml:"mcp"`        // MCP 协议配置
	RateLimit         RateLimitConfig  `yaml:"rate_limit"` // 速率限制配置
}

// GatewayConfig 配置 HTTP 网关。
type GatewayConfig struct {
	Addr  string `yaml:"addr"`  // 监听地址，如 ":8080"
	Token string `yaml:"token"` // Bearer token（空表示不认证）
}

// ProviderConfig 描述一个 LLM provider。
type ProviderConfig struct {
	ID      string        `yaml:"id"`
	Type    string        `yaml:"type"`     // openai（默认）
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
	Tier    string        `yaml:"tier"` // fast / balanced / powerful（用于路由模式）
}

// CronJobConfig 描述一个定时任务。
type CronJobConfig struct {
	ID       string `yaml:"id"`
	Schedule string `yaml:"schedule"` // "every 5m" / "every 1h"
	Prompt   string `yaml:"prompt"`
}

// MemoryConfig 配置内存检索。
type MemoryConfig struct {
	Dir             string `yaml:"dir"`
	Enabled         bool   `yaml:"enabled"`
	EmbeddingModel  string `yaml:"embedding_model"`   // embedding 模型（空则不启用向量搜索）
	EmbeddingURL    string `yaml:"embedding_url"`     // embedding API 地址（默认复用 provider）
	EmbeddingAPIKey string `yaml:"embedding_api_key"` // embedding API key（默认复用 provider）
}

// ChannelsConfig 聊天渠道总配置。
type ChannelsConfig struct {
	WeCom    WeComChannelConfig    `yaml:"wecom"`
	DingTalk DingTalkChannelConfig `yaml:"dingtalk"`
	Feishu   FeishuChannelConfig   `yaml:"feishu"`
}

// WeComChannelConfig 企业微信应用消息渠道配置。
type WeComChannelConfig struct {
	Enabled        bool   `yaml:"enabled"`
	CorpID         string `yaml:"corp_id"`
	AgentID        int    `yaml:"agent_id"`
	Secret         string `yaml:"secret"`
	Token          string `yaml:"token"`
	EncodingAESKey string `yaml:"encoding_aes_key"`
}

// DingTalkChannelConfig 钉钉机器人渠道配置。
type DingTalkChannelConfig struct {
	Enabled   bool   `yaml:"enabled"`
	AppKey    string `yaml:"app_key"`
	AppSecret string `yaml:"app_secret"`
	RobotCode string `yaml:"robot_code"` // 机器人编码（可选）
}

// FeishuChannelConfig 飞书机器人渠道配置。
type FeishuChannelConfig struct {
	Enabled           bool   `yaml:"enabled"`
	AppID             string `yaml:"app_id"`
	AppSecret         string `yaml:"app_secret"`
	VerificationToken string `yaml:"verification_token"`
	EncryptKey        string `yaml:"encrypt_key"` // 可选
}

// ACLConfig 访问控制配置。
type ACLConfig struct {
	Enabled       bool     `yaml:"enabled"`
	DefaultPolicy string   `yaml:"default_policy"` // "allow"（默认）或 "deny"
	Admins        []string `yaml:"admins"`          // 管理员: ["platform:userID", ...]
	AllowUsers    []string `yaml:"allow_users"`     // 白名单
	DenyUsers     []string `yaml:"deny_users"`      // 黑名单
	DenyTools     []string `yaml:"deny_tools"`      // 非管理员禁用的工具
}

// MCPConfig MCP 协议服务端配置。
type MCPConfig struct {
	Enabled bool   `yaml:"enabled"`
	Mode    string `yaml:"mode"` // "http"（默认）或 "stdio"
}

// RateLimitConfig 速率限制配置。
type RateLimitConfig struct {
	Enabled        bool    `yaml:"enabled"`
	RequestsPerSec float64 `yaml:"requests_per_sec"` // 每秒最大请求数（per IP/用户）
	Burst          int     `yaml:"burst"`             // 突发容量
	TokenQuota     int     `yaml:"token_quota"`       // 每用户每日 token 配额（0 = 不限）
}

// Load 从 YAML 文件加载配置并应用默认值。
// 若文件不存在则使用零值配置+默认值。
// 支持 ${ENV_VAR} 环境变量展开。
func Load(path string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// 文件不存在时使用全默认值
	} else {
		// 展开环境变量
		expanded := expandEnvVars(string(data))
		if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
			return nil, err
		}
	}
	cfg.applyDefaults()
	return cfg, nil
}

// envVarPattern 匹配 ${VAR} 和 $VAR 形式的环境变量引用。
var envVarPattern = regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}|\$([a-zA-Z_][a-zA-Z0-9_]*)`)

// expandEnvVars 展开字符串中的环境变量引用。
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// 提取变量名
		var name string
		if match[1] == '{' {
			name = match[2 : len(match)-1] // ${VAR}
		} else {
			name = match[1:] // $VAR
		}
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match // 未找到则保留原样
	})
}

func (c *Config) applyDefaults() {
	if c.Gateway.Addr == "" {
		c.Gateway.Addr = ":8080"
	}
	if c.WorkspaceDir == "" {
		c.WorkspaceDir, _ = os.Getwd()
	}
	if c.MaxContextTokens <= 0 {
		c.MaxContextTokens = 100_000
	}
	for i := range c.Providers {
		c.applyProviderDefaults(&c.Providers[i])
	}
	// 若只配置了 provider（单数），合并进 providers 列表
	if c.Provider.Model != "" {
		if len(c.Providers) > 0 {
			slog.Warn("both 'provider' and 'providers' are set; 'provider' (singular) will be ignored")
		} else {
			c.applyProviderDefaults(&c.Provider)
			c.Providers = append(c.Providers, c.Provider)
		}
	}
}

func (c *Config) applyProviderDefaults(p *ProviderConfig) {
	if p.Timeout == 0 {
		p.Timeout = 120 * time.Second
	}
	if p.Type == "" {
		p.Type = "openai"
	}
}
