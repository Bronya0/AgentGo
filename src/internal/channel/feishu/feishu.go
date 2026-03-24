// Package feishu 实现飞书（Lark）事件订阅消息回调渠道。
//
// 使用方式：
//  1. 在飞书开放平台 (https://open.feishu.cn) 创建企业自建应用
//  2. 开启机器人能力
//  3. 在「事件订阅」中配置请求地址为 http://your-server:8080/channel/feishu/callback
//  4. 订阅 im.message.receive_v1 事件
//  5. 记录 App ID、App Secret、Verification Token 和 Encrypt Key 填入配置
//  6. 启动 agent，用户在飞书中与机器人单聊即可交互
//
// 参考文档：https://open.feishu.cn/document/server-docs/im-v1/message/events/receive
package feishu

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bronya/mini-agent/internal/channel"
)

// Config 是飞书渠道的配置。
type Config struct {
	AppID             string `yaml:"app_id"`             // 应用 App ID
	AppSecret         string `yaml:"app_secret"`         // 应用 App Secret
	VerificationToken string `yaml:"verification_token"` // 事件订阅 Verification Token
	EncryptKey        string `yaml:"encrypt_key"`        // 事件订阅 Encrypt Key（可选）
}

// Channel 是飞书渠道实现。
type Channel struct {
	config  Config
	handler *channel.Handler
	tm      *tokenManager
}

// New 创建飞书渠道。
func New(cfg Config, handler *channel.Handler) *Channel {
	return &Channel{
		config:  cfg,
		handler: handler,
		tm:      newTokenManager(cfg.AppID, cfg.AppSecret),
	}
}

func (c *Channel) Name() string { return "feishu" }

// RegisterRoutes 注册飞书回调路由。
func (c *Channel) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/channel/feishu/callback", c.handleCallback)
}

// --- 事件结构 ---

// encryptedBody 是加密模式下飞书推送的消息体。
type encryptedBody struct {
	Encrypt string `json:"encrypt"`
}

// eventBody 是飞书 v2 事件的通用结构。
type eventBody struct {
	Schema    string          `json:"schema"` // "2.0"
	Challenge string          `json:"challenge"` // URL 验证用
	Token     string          `json:"token"`     // verification_token
	Type      string          `json:"type"`      // "url_verification" 或空
	Header    *eventHeader    `json:"header"`
	Event     json.RawMessage `json:"event"`
}

type eventHeader struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	CreateTime string `json:"create_time"`
	Token      string `json:"token"`
	AppID      string `json:"app_id"`
	TenantKey  string `json:"tenant_key"`
}

type messageEvent struct {
	Sender struct {
		SenderID struct {
			OpenID string `json:"open_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"` // "user"
	} `json:"sender"`
	Message struct {
		MessageID   string `json:"message_id"`
		ChatID      string `json:"chat_id"`
		ChatType    string `json:"chat_type"` // "p2p" 或 "group"
		Content     string `json:"content"`   // JSON 字符串: {"text":"xxx"}
		MessageType string `json:"message_type"` // "text"
	} `json:"message"`
}

type textContent struct {
	Text string `json:"text"`
}

// handleCallback 处理飞书的事件回调。
func (c *Channel) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 如果配置了 EncryptKey，先解密
	var rawJSON []byte
	if c.config.EncryptKey != "" {
		var enc encryptedBody
		if err := json.Unmarshal(body, &enc); err == nil && enc.Encrypt != "" {
			decrypted, err := c.decrypt(enc.Encrypt)
			if err != nil {
				slog.Error("feishu: decrypt failed", "err", err)
				http.Error(w, "decrypt failed", http.StatusBadRequest)
				return
			}
			rawJSON = decrypted
		} else {
			rawJSON = body
		}
	} else {
		rawJSON = body
	}

	var event eventBody
	if err := json.Unmarshal(rawJSON, &event); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// URL 验证（首次配置事件订阅时飞书会发此请求）
	if event.Type == "url_verification" {
		if event.Token != c.config.VerificationToken {
			http.Error(w, "token mismatch", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"challenge": event.Challenge})
		return
	}

	// 验证 token
	headerToken := ""
	if event.Header != nil {
		headerToken = event.Header.Token
	}
	if headerToken != "" && headerToken != c.config.VerificationToken {
		http.Error(w, "token mismatch", http.StatusForbidden)
		return
	}

	// 处理消息事件
	if event.Header == nil || event.Header.EventType != "im.message.receive_v1" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var msgEvent messageEvent
	if err := json.Unmarshal(event.Event, &msgEvent); err != nil {
		slog.Error("feishu: parse message event failed", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 只处理文本消息
	if msgEvent.Message.MessageType != "text" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 解析文本内容（Content 是 JSON 字符串）
	var tc textContent
	if err := json.Unmarshal([]byte(msgEvent.Message.Content), &tc); err != nil {
		slog.Error("feishu: parse text content failed", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if tc.Text == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	slog.Info("feishu: received message",
		"from", msgEvent.Sender.SenderID.OpenID,
		"chatType", msgEvent.Message.ChatType,
		"content", truncateLog(tc.Text, 50),
	)

	// 先返回 200
	w.WriteHeader(http.StatusOK)

	openID := msgEvent.Sender.SenderID.OpenID
	chatID := msgEvent.Message.ChatID
	text := tc.Text

	go func() {
		reply := c.handler.HandleMessage(r.Context(), channel.Message{
			UserID:    openID,
			Content:   text,
			SessionID: "feishu-" + chatID,
			Platform:  "feishu",
		}, c)

		if err := c.replyMessage(openID, reply); err != nil {
			slog.Error("feishu: send reply failed", "user", openID, "err", err)
		}
	}()
}

// replyMessage 通过飞书 API 发送文本消息。
func (c *Channel) replyMessage(openID, content string) error {
	token, err := c.tm.getToken()
	if err != nil {
		return fmt.Errorf("get tenant_access_token: %w", err)
	}

	// 飞书消息限制
	if len(content) > 30000 {
		content = content[:30000] + "\n...[消息过长已截断]"
	}

	payload := map[string]any{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    fmt.Sprintf(`{"text":%q}`, content),
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("API error %d: %s", result.Code, result.Msg)
	}
	return nil
}

// --- 解密工具 ---

// decrypt 解密飞书加密事件。
// 算法：AES-256-CBC，key = SHA256(encrypt_key)，IV = 前 16 字节密文。
func (c *Channel) decrypt(encryptedB64 string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	keyHash := sha256.Sum256([]byte(c.config.EncryptKey))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}

	if len(ciphertext) < aes.BlockSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(ciphertext, ciphertext)

	// PKCS5 去填充
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	padLen := int(ciphertext[len(ciphertext)-1])
	if padLen < 1 || padLen > aes.BlockSize || padLen > len(ciphertext) {
		return nil, fmt.Errorf("invalid padding")
	}
	return ciphertext[:len(ciphertext)-padLen], nil
}

func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- Token 管理 ---

type tokenManager struct {
	appID     string
	appSecret string
	token     string
	expireAt  time.Time
	mu        sync.Mutex
	client    *http.Client
}

func newTokenManager(appID, appSecret string) *tokenManager {
	return &tokenManager{
		appID:     appID,
		appSecret: appSecret,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// getToken 获取 tenant_access_token。
func (t *tokenManager) getToken() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Add(5*time.Minute).Before(t.expireAt) {
		return t.token, nil
	}

	payload := map[string]string{
		"app_id":     t.appID,
		"app_secret": t.appSecret,
	}
	jsonBody, _ := json.Marshal(payload)

	resp, err := t.client.Post(
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		"application/json; charset=utf-8",
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return "", fmt.Errorf("request tenant_access_token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"` // 秒
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("feishu API error %d: %s", result.Code, result.Msg)
	}

	t.token = result.TenantAccessToken
	t.expireAt = time.Now().Add(time.Duration(result.Expire) * time.Second)
	return t.token, nil
}
