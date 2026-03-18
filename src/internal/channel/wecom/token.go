package wecom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenManager 管理企业微信 access_token 的获取和缓存。
type tokenManager struct {
	corpID   string
	secret   string
	token    string
	expireAt time.Time
	mu       sync.Mutex
	client   *http.Client
}

func newTokenManager(corpID, secret string) *tokenManager {
	return &tokenManager{
		corpID: corpID,
		secret: secret,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// getToken 获取有效的 access_token（自动缓存和刷新）。
func (t *tokenManager) getToken() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 还有 5 分钟以上有效期，直接返回缓存
	if t.token != "" && time.Now().Add(5*time.Minute).Before(t.expireAt) {
		return t.token, nil
	}

	// 请求新 token
	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		t.corpID, t.secret)

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
		return "", fmt.Errorf("wecom API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	t.token = result.AccessToken
	t.expireAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	return t.token, nil
}

// sendTextMessage 通过企业微信 API 发送文本消息。
func sendTextMessage(tm *tokenManager, agentID int, userID, content string) error {
	token, err := tm.getToken()
	if err != nil {
		return fmt.Errorf("get access_token: %w", err)
	}

	// 企微消息内容限制 2048 字节，超长需截断
	if len(content) > 2000 {
		content = content[:2000] + "\n...[消息过长已截断]"
	}

	payload := map[string]any{
		"touser":  userID,
		"msgtype": "text",
		"agentid": agentID,
		"text": map[string]string{
			"content": content,
		},
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s", token)
	resp, err := tm.client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode send response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("send message API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	return nil
}
