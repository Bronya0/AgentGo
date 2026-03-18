// Package tool — 通知工具：通过 Webhook 发送通知。
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WebhookNotify 通过 webhook 发送通知（支持企业微信、钉钉、飞书等）。
func WebhookNotify() Tool {
	client := &http.Client{Timeout: 15 * time.Second}

	return Tool{
		Name:        "webhook_notify",
		Description: "Send a notification via webhook (supports WeCom/DingTalk/Feishu/Slack/custom). Use this to push code review results or alerts.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"webhook_url": map[string]any{
					"type":        "string",
					"description": "The webhook URL to send the notification to",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The message content (markdown format supported)",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional title for the notification",
				},
			},
			"required": []string{"webhook_url", "content"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			webhookURL, err := MustGetString(args, "webhook_url")
			if err != nil {
				return Errf("%v", err)
			}
			content, err := MustGetString(args, "content")
			if err != nil {
				return Errf("%v", err)
			}
			title, _ := args["title"].(string)

			// 构建通用 JSON payload（兼容多种 webhook 格式）
			payload := buildWebhookPayload(title, content)

			jsonBody, err := json.Marshal(payload)
			if err != nil {
				return Errf("marshal payload: %v", err)
			}

			req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(jsonBody))
			if err != nil {
				return Errf("create request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return Errf("webhook request failed: %v", err)
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			if resp.StatusCode >= 400 {
				return Errf("webhook returned HTTP %d: %s", resp.StatusCode, string(respBody))
			}

			return OK(fmt.Sprintf("通知已发送 (HTTP %d)", resp.StatusCode))
		},
	}
}

// buildWebhookPayload 构建 webhook 消息体。
// 尝试兼容企业微信 Bot / 钉钉 / 飞书 / Slack 格式。
func buildWebhookPayload(title, content string) map[string]any {
	text := content
	if title != "" {
		text = fmt.Sprintf("## %s\n\n%s", title, content)
	}

	return map[string]any{
		// 企业微信 Bot 格式
		"msgtype": "markdown",
		"markdown": map[string]any{
			"content": text,
		},
		// 钉钉格式（同 key 不冲突）
		"msg_type": "interactive",
		// 通用 text 字段
		"text": text,
	}
}
