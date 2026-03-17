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
}

// CronJobConfig 描述一个定时任务。
type CronJobConfig struct {
	ID       string `yaml:"id"`
	Schedule string `yaml:"schedule"` // "every 5m" / "every 1h"
	Prompt   string `yaml:"prompt"`
}

// MemoryConfig 配置内存检索。
type MemoryConfig struct {
	Dir     string `yaml:"dir"`
	Enabled bool   `yaml:"enabled"`
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
