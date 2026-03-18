// Package channel 定义聊天渠道（Channel）的通用接口。
//
// Channel 是连接外部聊天平台（企业微信、钉钉、飞书等）与 Agent 的适配层。
// 每个 Channel 负责：
//   - 接收平台推送的用户消息
//   - 转换为内部格式交给 Runner 处理
//   - 将 Agent 响应转换回平台格式并发送
package channel

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bronya/mini-agent/internal/acl"
	"github.com/bronya/mini-agent/internal/runner"
	"github.com/bronya/mini-agent/internal/session"
)

// Message 是渠道收到的用户消息。
type Message struct {
	UserID    string // 平台用户 ID
	UserName  string // 用户名（可能为空）
	Content   string // 消息文本
	SessionID string // 会话标识（可自动从 UserID 映射）
	Platform  string // 来源平台标识
}

// Reply 是渠道要发送的回复。
type Reply struct {
	UserID  string
	Content string
}

// Channel 是聊天渠道接口。
type Channel interface {
	// Name 返回渠道名称（如 "wecom", "dingtalk"）。
	Name() string
	// RegisterRoutes 注册 HTTP 路由到 mux（用于接收回调/webhook）。
	RegisterRoutes(mux *http.ServeMux)
}

// Handler 封装 Agent Runner 和 Session 池，供各个 Channel 共用。
type Handler struct {
	Runner   *runner.Runner
	Sessions *session.Pool
	ACL      *acl.Service // 可选，nil 表示不做权限检查
}

// HandleMessage 处理来自任意渠道的消息，返回 Agent 的完整文本回复。
func (h *Handler) HandleMessage(ctx context.Context, msg Message) string {
	// ACL 用户级权限检查
	if h.ACL != nil && !h.ACL.CanAccess(msg.Platform, msg.UserID) {
		slog.Warn("channel: access denied",
			"platform", msg.Platform,
			"user", msg.UserID,
		)
		return "抱歉，您没有权限使用此服务。请联系管理员开通。"
	}

	sessionID := msg.SessionID
	if sessionID == "" {
		sessionID = msg.Platform + "-" + msg.UserID
	}

	sess := h.Sessions.Get(sessionID)

	// 将用户身份存入 context，供下游（如工具级 ACL）使用
	ctx = acl.WithUser(ctx, acl.UserIdentity{
		Platform: msg.Platform,
		UserID:   msg.UserID,
	})

	var reply string
	err := h.Runner.Run(ctx, sess, msg.Content, func(chunk runner.StreamChunk) {
		switch chunk.Event {
		case runner.EventText:
			reply += chunk.Text
		case runner.EventError:
			if chunk.Err != nil {
				slog.Error("channel message error",
					"platform", msg.Platform,
					"user", msg.UserID,
					"err", chunk.Err,
				)
			}
		}
	})
	if err != nil {
		slog.Error("channel run failed",
			"platform", msg.Platform,
			"user", msg.UserID,
			"err", err,
		)
		if reply == "" {
			reply = "处理消息时出错，请稍后重试。"
		}
	}

	return reply
}
