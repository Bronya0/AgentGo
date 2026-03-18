// Package ratelimit 提供基于令牌桶算法的请求速率限制。
//
// 速率限制使用 golang.org/x/time/rate 标准实现；
// token 配额管理（每用户每日上限）为业务逻辑，自行维护。
package ratelimit

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Config 是速率限制配置。
type Config struct {
	Enabled        bool    `yaml:"enabled"`
	RequestsPerSec float64 `yaml:"requests_per_sec"` // 每秒最大请求数（per-key）
	Burst          int     `yaml:"burst"`             // 突发容量
	TokenQuota     int     `yaml:"token_quota"`       // 每用户 token 配额（0 = 不限）
	QuotaWindow    string  `yaml:"quota_window"`      // 配额窗口：daily（默认）
}

// quota 跟踪每用户的 token 消耗配额。
type quota struct {
	used      int
	limit     int
	windowEnd time.Time
}

// entry 持有某个 key 对应的限流器及最后使用时间。
type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter 是速率限制器。
type Limiter struct {
	cfg     Config
	mu      sync.Mutex
	clients map[string]*entry
	quotas  map[string]*quota
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
		clients: make(map[string]*entry),
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

	e, ok := l.clients[key]
	if !ok {
		e = &entry{
			limiter: rate.NewLimiter(rate.Limit(l.cfg.RequestsPerSec), l.cfg.Burst),
		}
		l.clients[key] = e
	}
	e.lastSeen = time.Now()
	return e.limiter.Allow()
}

// ConsumeTokens 消耗 token 配额，返回是否在配额内。
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

// TokensRemaining 返回 key 剩余的 token 配额（-1 表示不限）。
func (l *Limiter) TokensRemaining(key string) int {
	if !l.cfg.Enabled || l.cfg.TokenQuota <= 0 {
		return -1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	q, ok := l.quotas[key]
	if !ok || time.Now().After(q.windowEnd) {
		return l.cfg.TokenQuota
	}
	if remaining := q.limit - q.used; remaining > 0 {
		return remaining
	}
	return 0
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
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}

// nextWindow 返回下一个配额窗口结束时间（次日 00:00:00）。
func nextWindow() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

// cleanup 每 5 分钟清理 10 分钟未使用的条目。
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for key, e := range l.clients {
			if now.Sub(e.lastSeen) > 10*time.Minute {
				delete(l.clients, key)
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
