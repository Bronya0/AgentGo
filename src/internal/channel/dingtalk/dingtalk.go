// Package dingtalk 实现钉钉企业内部机器人消息回调渠道。
//
// 使用方式：
//  1. 在钉钉开放平台 (https://open-dev.dingtalk.com) 创建企业内部应用
//  2. 开启机器人能力，配置消息接收地址为 http://your-server:8080/channel/dingtalk/callback
//  3. 记录 AppKey、AppSecret 填入配置文件
//  4. 启动 agent，用户在钉钉中 @机器人 发消息即可与 agent 交互
//
// 参考文档：https://open.dingtalk.com/document/orgapp/receive-message
package dingtalk

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bronya/mini-agent/internal/channel"
)

// Config 是钉钉渠道的配置。
type Config struct {
	AppKey    string `yaml:"app_key"`    // 应用 AppKey
	AppSecret string `yaml:"app_secret"` // 应用 AppSecret
	RobotCode string `yaml:"robot_code"` // 机器人编码（可选，用于签名验证）
}

// Channel 是钉钉渠道实现。
type Channel struct {
	config  Config
	handler *channel.Handler
	tm      *tokenManager
}

// New 创建钉钉渠道。
func New(cfg Config, handler *channel.Handler) *Channel {
	return &Channel{
		config:  cfg,
		handler: handler,
		tm:      newTokenManager(cfg.AppKey, cfg.AppSecret),
	}
}

func (c *Channel) Name() string { return "dingtalk" }

// RegisterRoutes 注册钉钉回调路由。
func (c *Channel) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/channel/dingtalk/callback", c.handleCallback)
}

// --- 回调消息结构 ---

// callbackMsg 是钉钉推送到回调 URL 的消息体。
type callbackMsg struct {
	ConversationType string `json:"conversationType"` // "1"=单聊, "2"=群聊
	AtUsers          []struct {
		DingtalkID string `json:"dingtalkId"`
	} `json:"atUsers"`
	ChatbotCorpID             string `json:"chatbotCorpId"`
	ChatbotUserID             string `json:"chatbotUserId"`
	MsgID                     string `json:"msgId"`
	SenderNick                string `json:"senderNick"`
	IsAdmin                   bool   `json:"isAdmin"`
	SenderStaffID             string `json:"senderStaffId"`
	SessionWebhookExpiredTime int64  `json:"sessionWebhookExpiredTime"`
	CreateAt                  int64  `json:"createAt"`
	SenderCorpID              string `json:"senderCorpId"`
	ConversationID            string `json:"conversationId"`
	IsInAtList                bool   `json:"isInAtList"`
	SessionWebhook            string `json:"sessionWebhook"` // 用于回复的临时 webhook
	Text                      struct {
		Content string `json:"content"`
	} `json:"text"`
	MsgType string `json:"msgtype"` // "text"
}

// handleCallback 处理钉钉的消息回调。
func (c *Channel) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证签名（如果配置了 AppSecret）
	if c.config.AppSecret != "" {
		timestamp := r.Header.Get("timestamp")
		sign := r.Header.Get("sign")
		if !c.verifySignature(timestamp, sign) {
			slog.Warn("dingtalk: signature verification failed")
			http.Error(w, "signature verification failed", http.StatusForbidden)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var msg callbackMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// 只处理文本消息
	if msg.MsgType != "text" {
		w.WriteHeader(http.StatusOK)
		return
	}

	content := strings.TrimSpace(msg.Text.Content)
	if content == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	slog.Info("dingtalk: received message",
		"from", msg.SenderNick,
		"staffId", msg.SenderStaffID,
		"content", truncateLog(content, 50),
	)

	// 先返回 200（钉钉要求快速响应）
	w.WriteHeader(http.StatusOK)

	// 异步处理
	sessionWebhook := msg.SessionWebhook
	senderStaffID := msg.SenderStaffID
	conversationID := msg.ConversationID

	go func() {
		reply := c.handler.HandleMessage(r.Context(), channel.Message{
			UserID:    senderStaffID,
			UserName:  msg.SenderNick,
			Content:   content,
			SessionID: "dingtalk-" + conversationID,
			Platform:  "dingtalk",
		})

		// 优先使用 sessionWebhook 回复（有效期内）
		if sessionWebhook != "" {
			if err := replyViaWebhook(sessionWebhook, reply); err != nil {
				slog.Error("dingtalk: webhook reply failed, falling back to API",
					"err", err,
				)
				// 回退到主动消息 API
				c.sendMessage(senderStaffID, reply)
			}
			return
		}
		// 没有 webhook 时通过 API 主动发送
		c.sendMessage(senderStaffID, reply)
	}()
}

// sendMessage 通过钉钉工作通知 API 发送消息。
func (c *Channel) sendMessage(userID, content string) {
	if err := sendWorkNotice(c.tm, userID, content); err != nil {
		slog.Error("dingtalk: send message failed", "user", userID, "err", err)
	}
}

// verifySignature 验证钉钉回调签名。
// sign = Base64(HmacSHA256(timestamp + "\n" + appSecret, appSecret))
func (c *Channel) verifySignature(timestamp, sign string) bool {
	if timestamp == "" || sign == "" {
		return false
	}

	// 检查时间戳（防止重放，允许 1 小时偏差）
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	diff := time.Now().UnixMilli() - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > 3600_000 {
		return false
	}

	stringToSign := timestamp + "\n" + c.config.AppSecret
	mac := hmac.New(sha256.New, []byte(c.config.AppSecret))
	mac.Write([]byte(stringToSign))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(sign))
}

// replyViaWebhook 使用 sessionWebhook 回复消息。
func replyViaWebhook(webhookURL, content string) error {
	// 截断过长消息
	if len(content) > 20000 {
		content = content[:20000] + "\n...[消息过长已截断]"
	}

	payload := map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": content,
		},
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- Token 管理 ---

type tokenManager struct {
	appKey    string
	appSecret string
	token     string
	expireAt  time.Time
	mu        sync.Mutex
	client    *http.Client
}

func newTokenManager(appKey, appSecret string) *tokenManager {
	return &tokenManager{
		appKey:    appKey,
		appSecret: appSecret,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *tokenManager) getToken() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Add(5*time.Minute).Before(t.expireAt) {
		return t.token, nil
	}

	url := fmt.Sprintf("https://oapi.dingtalk.com/gettoken?appkey=%s&appsecret=%s",
		t.appKey, t.appSecret)

	resp, err := t.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("request access_token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("dingtalk API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	t.token = result.AccessToken
	t.expireAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return t.token, nil
}

// sendWorkNotice 通过钉钉工作通知 API 发送消息给指定用户。
func sendWorkNotice(tm *tokenManager, userID, content string) error {
	token, err := tm.getToken()
	if err != nil {
		return fmt.Errorf("get access_token: %w", err)
	}

	// 钉钉工作通知限制
	if len(content) > 20000 {
		content = content[:20000] + "\n...[消息过长已截断]"
	}

	payload := map[string]any{
		"userid_list": userID,
		"msg": map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": content,
			},
		},
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	apiURL := fmt.Sprintf("https://oapi.dingtalk.com/topapi/message/corpconversation/asyncsend_v2?access_token=%s", token)
	resp, err := tm.client.Post(apiURL, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("send work notice: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("API error %d: %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}
