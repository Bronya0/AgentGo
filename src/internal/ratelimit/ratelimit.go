// Package ratelimit 提供基于令牌桶算法的请求速率限制。
//
// 支持：
//   - 全局速率限制（每秒最大请求数）
//   - 按用户/IP 分别限制
//   - Token 配额管理（每用户每日/总量消耗上限）
//   - 自动清理过期条目
package ratelimit

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config 是速率限制配置。
type Config struct {
	Enabled        bool    `yaml:"enabled"`
	RequestsPerSec float64 `yaml:"requests_per_sec"` // 每秒最大请求数（per-key）
	Burst          int     `yaml:"burst"`             // 突发容量
	TokenQuota     int     `yaml:"token_quota"`       // 每用户 token 配额（0 = 不限）
	QuotaWindow    string  `yaml:"quota_window"`      // 配额窗口：daily（默认）
}

// Limiter 是速率限制器。
type Limiter struct {
	cfg     Config
	mu      sync.Mutex
	buckets map[string]*bucket
	quotas  map[string]*quota
}

type bucket struct {
	tokens   float64
	capacity float64
	rate     float64 // tokens per second
	lastTime time.Time
}

type quota struct {
	used      int
	limit     int
	windowEnd time.Time
}

// New 创建一个 Limiter。
func New(cfg Config) *Limiter {
	if cfg.RequestsPerSec <= 0 {
		cfg.RequestsPerSec = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	l := &Limiter{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
		quotas:  make(map[string]*quota),
	}
	go l.cleanup()
	return l
}

// Allow 判断给定 key（通常为 IP 或 userID）是否被允许请求。
func (l *Limiter) Allow(key string) bool {
	if !l.cfg.Enabled {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{
			tokens:   float64(l.cfg.Burst),
			capacity: float64(l.cfg.Burst),
			rate:     l.cfg.RequestsPerSec,
			lastTime: time.Now(),
		}
		l.buckets[key] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastTime = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ConsumeTokens 消耗 token 配额，返回是否在配额内。
// tokens 参数为本次消耗的 token 数。
func (l *Limiter) ConsumeTokens(key string, tokens int) bool {
	if !l.cfg.Enabled || l.cfg.TokenQuota <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	q, ok := l.quotas[key]
	if !ok || time.Now().After(q.windowEnd) {
		q = &quota{
			limit:     l.cfg.TokenQuota,
			windowEnd: nextWindow(),
		}
		l.quotas[key] = q
	}

	if q.used+tokens > q.limit {
		return false
	}
	q.used += tokens
	return true
}

// TokensRemaining 返回 key 剩余的 token 配额。
func (l *Limiter) TokensRemaining(key string) int {
	if !l.cfg.Enabled || l.cfg.TokenQuota <= 0 {
		return -1 // -1 表示不限
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	q, ok := l.quotas[key]
	if !ok || time.Now().After(q.windowEnd) {
		return l.cfg.TokenQuota
	}
	remaining := q.limit - q.used
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// Middleware 返回一个 HTTP 中间件，自动按客户端 IP 限流。
func (l *Limiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	if !l.cfg.Enabled {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r)
		if !l.Allow(key) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// clientIP 提取客户端 IP。
func clientIP(r *http.Request) string {
	// 优先从 X-Forwarded-For 取第一个 IP
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// 回退到 RemoteAddr
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}

// nextWindow 返回下一个配额窗口结束时间（当日 23:59:59）。
func nextWindow() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

// cleanup 每 5 分钟清理 10 分钟未使用的桶。
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for key, b := range l.buckets {
			if now.Sub(b.lastTime) > 10*time.Minute {
				delete(l.buckets, key)
			}
		}
		for key, q := range l.quotas {
			if now.After(q.windowEnd) {
				delete(l.quotas, key)
			}
		}
		l.mu.Unlock()
	}
}
