// Package wecom 实现企业微信消息回调渠道。
//
// 使用方式：
//  1. 在企业微信管理后台创建自建应用
//  2. 配置"接收消息"的 URL 为 http://your-server:8080/channel/wecom/callback
//  3. 填入 Token 和 EncodingAESKey 到配置文件
//  4. 启动 agent，用户在企微中发消息即可与 agent 交互
//
// 参考文档：https://developer.work.weixin.qq.com/document/path/90238
package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/bronya/mini-agent/internal/channel"
)

// Config 是企业微信渠道的配置。
type Config struct {
	CorpID        string `yaml:"corp_id"`         // 企业 ID
	AgentID       int    `yaml:"agent_id"`         // 应用 AgentID
	Secret        string `yaml:"secret"`           // 应用 Secret
	Token         string `yaml:"token"`            // 回调 Token
	EncodingAESKey string `yaml:"encoding_aes_key"` // 回调 EncodingAESKey
}

// Channel 是企业微信渠道实现。
type Channel struct {
	config  Config
	handler *channel.Handler
	aesKey  []byte
	tm      *tokenManager
}

// New 创建企业微信渠道。
func New(cfg Config, handler *channel.Handler) (*Channel, error) {
	// EncodingAESKey 是 Base64 编码的 AES key（43 字符 → 32 字节）
	aesKey, err := base64.StdEncoding.DecodeString(cfg.EncodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("decode encoding_aes_key: %w", err)
	}
	if len(aesKey) != 32 {
		return nil, fmt.Errorf("encoding_aes_key must decode to 32 bytes, got %d", len(aesKey))
	}
	return &Channel{
		config:  cfg,
		handler: handler,
		aesKey:  aesKey,
		tm:      newTokenManager(cfg.CorpID, cfg.Secret),
	}, nil
}

func (c *Channel) Name() string { return "wecom" }

// RegisterRoutes 注册企业微信回调路由。
func (c *Channel) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/channel/wecom/callback", c.handleCallback)
}

// --- XML 消息结构 ---

type xmlRecvMsg struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	Encrypt    string   `xml:"Encrypt"`
	AgentID    string   `xml:"AgentID"`
}

type xmlDecryptedMsg struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgId        int64    `xml:"MsgId"`
}

type xmlReplyMsg struct {
	XMLName      xml.Name `xml:"xml"`
	Encrypt      string   `xml:"Encrypt"`
	MsgSignature string   `xml:"MsgSignature"`
	TimeStamp    string   `xml:"TimeStamp"`
	Nonce        string   `xml:"Nonce"`
}

// handleCallback 处理企业微信的回调请求。
// - GET: URL 验证（首次配置时企微会发 GET 验证请求）
// - POST: 消息推送
func (c *Channel) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	msgSignature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")

	if r.Method == http.MethodGet {
		// URL 验证
		echoStr := query.Get("echostr")
		if !c.verifySignature(msgSignature, timestamp, nonce, echoStr) {
			http.Error(w, "signature verification failed", http.StatusForbidden)
			return
		}
		// 解密 echostr 并返回
		plaintext, err := c.decrypt(echoStr)
		if err != nil {
			http.Error(w, "decrypt echostr failed", http.StatusBadRequest)
			return
		}
		w.Write([]byte(plaintext))
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 读取请求体
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 解析加密的 XML
	var recvMsg xmlRecvMsg
	if err := xml.Unmarshal(body, &recvMsg); err != nil {
		http.Error(w, "invalid XML", http.StatusBadRequest)
		return
	}

	// 验证签名
	if !c.verifySignature(msgSignature, timestamp, nonce, recvMsg.Encrypt) {
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	// 解密消息
	plaintext, err := c.decrypt(recvMsg.Encrypt)
	if err != nil {
		slog.Error("wecom: decrypt message failed", "err", err)
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}

	// 解析明文 XML
	var msg xmlDecryptedMsg
	if err := xml.Unmarshal([]byte(plaintext), &msg); err != nil {
		slog.Error("wecom: parse decrypted message failed", "err", err)
		w.WriteHeader(http.StatusOK) // 企微要求返回 200
		return
	}

	// 只处理文本消息
	if msg.MsgType != "text" {
		slog.Debug("wecom: ignoring non-text message", "type", msg.MsgType)
		w.WriteHeader(http.StatusOK)
		return
	}

	slog.Info("wecom: received message", "from", msg.FromUserName, "content", truncateLog(msg.Content, 50))

	// 异步处理消息（企微要求 5 秒内返回，长时间处理需异步）
	// 先返回 200，再后台处理
	w.WriteHeader(http.StatusOK)

	go func() {
		reply := c.handler.HandleMessage(r.Context(), channel.Message{
			UserID:   msg.FromUserName,
			Content:  msg.Content,
			Platform: "wecom",
		})

		// 通过企微主动发消息 API 回复
		if err := c.sendMessage(msg.FromUserName, reply); err != nil {
			slog.Error("wecom: send reply failed", "user", msg.FromUserName, "err", err)
		}
	}()
}

// sendMessage 通过企业微信 API 主动发消息。
func (c *Channel) sendMessage(userID, content string) error {
	return sendTextMessage(c.tm, c.config.AgentID, userID, content)
}

// --- 加解密工具 ---

// verifySignature 验证企微消息签名。
func (c *Channel) verifySignature(msgSignature, timestamp, nonce, encrypt string) bool {
	strs := []string{c.config.Token, timestamp, nonce, encrypt}
	sort.Strings(strs)
	combined := strings.Join(strs, "")
	hash := sha1.Sum([]byte(combined))
	expected := fmt.Sprintf("%x", hash)
	return expected == msgSignature
}

// decrypt 解密企微消息。
func (c *Channel) decrypt(encryptedB64 string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	if len(ciphertext) < aes.BlockSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	iv := c.aesKey[:aes.BlockSize]
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(ciphertext, ciphertext)

	// PKCS7 去填充
	padLen := int(ciphertext[len(ciphertext)-1])
	if padLen < 1 || padLen > aes.BlockSize || padLen > len(ciphertext) {
		return "", fmt.Errorf("invalid PKCS7 padding")
	}
	plaintext := ciphertext[:len(ciphertext)-padLen]

	// 企微消息格式：16字节随机串 + 4字节消息长度(big-endian) + 消息内容 + CorpID
	if len(plaintext) < 20 {
		return "", fmt.Errorf("plaintext too short after removing padding")
	}
	msgLen := binary.BigEndian.Uint32(plaintext[16:20])
	if 20+int(msgLen) > len(plaintext) {
		return "", fmt.Errorf("invalid message length")
	}
	msg := string(plaintext[20 : 20+msgLen])

	return msg, nil
}

func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
