// Package acl 实现用户访问控制列表（ACL）。
//
// 支持三层权限控制：
//   - 用户级：通过白名单/黑名单控制谁能使用 Agent
//   - 工具级：限制非管理员用户可使用的工具
//   - 管理员：拥有全部权限
//
// 用户标识格式统一为 "platform:userID"，例如 "wecom:ZhangSan"、"dingtalk:staff123"。
package acl

import (
	"context"
	"strings"
)

// Config 是 ACL 配置。
type Config struct {
	Enabled       bool     `yaml:"enabled"`
	DefaultPolicy string   `yaml:"default_policy"` // "allow"（默认）或 "deny"
	Admins        []string `yaml:"admins"`          // 管理员列表: ["platform:userID", ...]
	AllowUsers    []string `yaml:"allow_users"`     // 白名单
	DenyUsers     []string `yaml:"deny_users"`      // 黑名单
	DenyTools     []string `yaml:"deny_tools"`      // 非管理员禁用的工具
}

// Service 提供 ACL 鉴权服务。
type Service struct {
	enabled       bool
	defaultAllow  bool
	admins        map[string]bool
	allowUsers    map[string]bool
	denyUsers     map[string]bool
	denyTools     map[string]bool
}

// NewService 从配置创建 ACL 服务。
func NewService(cfg Config) *Service {
	s := &Service{
		enabled:      cfg.Enabled,
		defaultAllow: cfg.DefaultPolicy != "deny",
		admins:       toSet(cfg.Admins),
		allowUsers:   toSet(cfg.AllowUsers),
		denyUsers:    toSet(cfg.DenyUsers),
		denyTools:    toSet(cfg.DenyTools),
	}
	return s
}

// CanAccess 检查用户是否可以访问 Agent。
// 如果 ACL 未启用，始终返回 true。
func (s *Service) CanAccess(platform, userID string) bool {
	if !s.enabled {
		return true
	}

	key := platform + ":" + userID

	// 管理员始终允许
	if s.admins[key] {
		return true
	}

	// 显式黑名单
	if s.denyUsers[key] {
		return false
	}

	// 显式白名单
	if s.allowUsers[key] {
		return true
	}

	// 如果配置了白名单且用户不在其中，默认拒绝
	if len(s.allowUsers) > 0 {
		return false
	}

	return s.defaultAllow
}

// CanUseTool 检查用户是否可以使用指定工具。
// 管理员不受工具限制。非管理员不能使用 deny_tools 中的工具。
func (s *Service) CanUseTool(platform, userID, toolName string) bool {
	if !s.enabled {
		return true
	}

	key := platform + ":" + userID

	// 管理员可以使用所有工具
	if s.admins[key] {
		return true
	}

	// 检查工具黑名单
	return !s.denyTools[toolName]
}

// IsAdmin 检查用户是否为管理员。
func (s *Service) IsAdmin(platform, userID string) bool {
	if !s.enabled {
		return false
	}
	return s.admins[platform+":"+userID]
}

// --- Context 传递用户身份 ---

type ctxKey struct{}

// UserIdentity 表示渠道中的用户身份。
type UserIdentity struct {
	Platform string
	UserID   string
}

// WithUser 将用户身份存入 context。
func WithUser(ctx context.Context, u UserIdentity) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// GetUser 从 context 中取出用户身份。
func GetUser(ctx context.Context) (UserIdentity, bool) {
	u, ok := ctx.Value(ctxKey{}).(UserIdentity)
	return u, ok
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[strings.TrimSpace(item)] = true
	}
	return m
}
