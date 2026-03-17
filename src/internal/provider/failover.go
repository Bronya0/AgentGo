// Package provider — FailoverProvider：按顺序尝试多个 provider，遇到 ErrFailover 时切换。
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FailoverProvider 封装多个 Provider，按优先级顺序 failover。
type FailoverProvider struct {
	providers []Provider
	mu        sync.Mutex
	cooldowns map[string]time.Time // provider ID -> 冷却结束时间
	cooldown  time.Duration        // 默认 60s
}

// NewFailover 创建一个 failover provider。
func NewFailover(providers ...Provider) *FailoverProvider {
	return &FailoverProvider{
		providers: providers,
		cooldowns: make(map[string]time.Time),
		cooldown:  60 * time.Second,
	}
}

// ID 返回"failover"。
func (f *FailoverProvider) ID() string { return "failover" }

// Model 返回当前首选 provider 的模型名。
func (f *FailoverProvider) Model() string {
	if p := f.primary(); p != nil {
		return p.Model()
	}
	return "unknown"
}

// Chat 尝试所有可用 provider，遇到 failover 错误时切换到下一个。
func (f *FailoverProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	handler StreamHandler,
) (*Message, error) {
	var lastErr error
	for _, p := range f.providers {
		if f.isCoolingDown(p.ID()) {
			continue
		}
		msg, err := p.Chat(ctx, messages, tools, handler)
		if err == nil {
			return msg, nil
		}
		if IsFailover(err) {
			f.startCooldown(p.ID())
			lastErr = err
			continue
		}
		// 非 failover 错误直接返回
		return nil, err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("all providers exhausted: %w", lastErr)
	}
	return nil, fmt.Errorf("no available providers")
}

func (f *FailoverProvider) primary() Provider {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, p := range f.providers {
		if t, ok := f.cooldowns[p.ID()]; !ok || now.After(t) {
			return p
		}
	}
	return nil
}

func (f *FailoverProvider) isCoolingDown(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.cooldowns[id]; ok {
		return time.Now().Before(t)
	}
	return false
}

func (f *FailoverProvider) startCooldown(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cooldowns[id] = time.Now().Add(f.cooldown)
}
