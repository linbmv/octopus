package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

// RateLimiter 实现简单的滑动窗口速率限制
type RateLimiter struct {
	mu        sync.RWMutex
	buckets   map[string]*bucket
	limit     int
	window    time.Duration
	cleanTTL  time.Duration
	lastClean time.Time
}

type bucket struct {
	requests []time.Time
	lastSeen time.Time
}

// NewRateLimiter 创建速率限制器
// limit: 时间窗口内最大请求数
// window: 时间窗口大小
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets:   make(map[string]*bucket),
		limit:     limit,
		window:    window,
		cleanTTL:  window * 10,
		lastClean: time.Now(),
	}
	return rl
}

// Allow 检查是否允许请求
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rl.cleanupLocked(now)
	b, exists := rl.buckets[key]
	if !exists {
		rl.buckets[key] = &bucket{
			requests: []time.Time{now},
			lastSeen: now,
		}
		return true
	}

	b.lastSeen = now
	cutoff := now.Add(-rl.window)
	validRequests := make([]time.Time, 0, len(b.requests))
	for _, t := range b.requests {
		if t.After(cutoff) {
			validRequests = append(validRequests, t)
		}
	}

	if len(validRequests) >= rl.limit {
		b.requests = validRequests
		return false
	}

	b.requests = append(validRequests, now)
	return true
}

// cleanupLocked performs bounded opportunistic cleanup on request traffic.
// This avoids a permanent goroutine created from route-registration init().
func (rl *RateLimiter) cleanupLocked(now time.Time) {
	if now.Sub(rl.lastClean) < rl.window {
		return
	}
	for key, b := range rl.buckets {
		if now.Sub(b.lastSeen) > rl.cleanTTL {
			delete(rl.buckets, key)
		}
	}
	rl.lastClean = now
}

// RateLimit 返回基于 IP 的速率限制中间件
func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	limiter := NewRateLimiter(limit, window)
	return func(c *gin.Context) {
		key := c.ClientIP()
		if !limiter.Allow(key) {
			resp.Error(c, http.StatusTooManyRequests, "rate limit exceeded")
			c.Abort()
			return
		}
		c.Next()
	}
}
