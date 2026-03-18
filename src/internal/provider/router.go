// Package provider — RouterProvider：根据任务复杂度自动路由到不同模型。
//
// 分类策略（基于消息特征的启发式规则）：
//   - fast: 简单问答、翻译、格式转换等无需工具的轻量任务
//   - balanced: 常规代码编辑、工具调用、中等复杂任务（默认）
//   - powerful: 多文件重构、架构设计、复杂调试等需要深度推理的任务
package provider

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// Tier 表示模型能力层级。
type Tier string

const (
	TierFast     Tier = "fast"
	TierBalanced Tier = "balanced"
	TierPowerful Tier = "powerful"
)

// RouterProvider 根据任务复杂度将请求路由到不同层级的 Provider。
type RouterProvider struct {
	tiers map[Tier]Provider
	// 分类函数，可自定义；为 nil 时使用默认启发式
	Classify func(messages []Message, tools []ToolDefinition) Tier
}

// RouterConfig 配置路由 provider。
type RouterConfig struct {
	Fast     Provider // 轻量/快速模型
	Balanced Provider // 默认模型
	Powerful Provider // 强力模型（复杂推理）
}

// NewRouter 创建路由 provider，至少需要 Balanced。
func NewRouter(cfg RouterConfig) *RouterProvider {
	tiers := make(map[Tier]Provider)
	if cfg.Fast != nil {
		tiers[TierFast] = cfg.Fast
	}
	if cfg.Balanced != nil {
		tiers[TierBalanced] = cfg.Balanced
	}
	if cfg.Powerful != nil {
		tiers[TierPowerful] = cfg.Powerful
	}
	return &RouterProvider{tiers: tiers}
}

func (r *RouterProvider) ID() string { return "router" }

func (r *RouterProvider) Model() string {
	if p, ok := r.tiers[TierBalanced]; ok {
		return "router:" + p.Model()
	}
	return "router:unknown"
}

// Chat 根据消息复杂度选择合适的 provider 进行调用。
func (r *RouterProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	handler StreamHandler,
) (*Message, error) {
	tier := r.classify(messages, tools)
	p := r.resolve(tier)
	slog.Debug("router selected tier", "tier", tier, "model", p.Model())
	return p.Chat(ctx, messages, tools, handler)
}

// classify 使用自定义或默认的分类逻辑。
func (r *RouterProvider) classify(messages []Message, tools []ToolDefinition) Tier {
	if r.Classify != nil {
		return r.Classify(messages, tools)
	}
	return defaultClassify(messages, tools)
}

// resolve 按层级选择 provider，缺失时降级到相邻层。
func (r *RouterProvider) resolve(tier Tier) Provider {
	if p, ok := r.tiers[tier]; ok {
		return p
	}
	// 降级策略：fast→balanced, powerful→balanced, balanced→任意
	if p, ok := r.tiers[TierBalanced]; ok {
		return p
	}
	for _, p := range r.tiers {
		return p
	}
	// 不应到达
	return nil
}

// --- 默认启发式分类 ---

// powerful 关键词：涉及复杂任务时优先使用强力模型。
var powerfulKeywords = []string{
	"重构", "架构", "设计模式", "性能优化", "安全审计",
	"refactor", "architect", "design pattern", "performance", "security audit",
	"debug complex", "多文件", "multi-file",
	"分析整个", "analyze entire", "全面检查", "comprehensive review",
}

// fast 关键词：简单任务用快速模型。
var fastKeywords = []string{
	"翻译", "translate",
	"你好", "hello", "hi",
	"时间", "日期", "what time", "what date",
	"帮我算", "calculate",
	"格式化", "format",
}

func defaultClassify(messages []Message, tools []ToolDefinition) Tier {
	// 找最后一条用户消息
	var lastUser string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			lastUser = messages[i].Content
			break
		}
	}

	if lastUser == "" {
		return TierBalanced
	}

	lower := strings.ToLower(lastUser)
	msgLen := utf8.RuneCountInString(lastUser)

	// 统计对话中的工具调用次数
	toolCallCount := 0
	for _, m := range messages {
		toolCallCount += len(m.ToolCalls)
	}

	// 规则 1: powerful 关键词匹配
	for _, kw := range powerfulKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return TierPowerful
		}
	}

	// 规则 2: 超长消息 + 有工具 → powerful
	if msgLen > 500 && len(tools) > 0 {
		return TierPowerful
	}

	// 规则 3: 已经进行了多轮工具调用 → powerful（复杂任务进行中）
	if toolCallCount > 6 {
		return TierPowerful
	}

	// 规则 4: fast 关键词 + 短消息 + 无需工具调用历史
	if msgLen < 100 && toolCallCount == 0 {
		for _, kw := range fastKeywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return TierFast
			}
		}
	}

	// 规则 5: 非常短的消息且无工具 → fast
	if msgLen < 30 && len(tools) == 0 {
		return TierFast
	}

	return TierBalanced
}
