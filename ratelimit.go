package main

import (
	"sync"
	"time"
)

type tokenBucket struct {
	capacity   float64
	tokens     float64
	refillRate float64
	last       time.Time
}

func (b *tokenBucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.last = now
		b.tokens = b.capacity
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

const bucketIdleTTL = 10 * time.Minute
const cleanupInterval = 1 * time.Minute

type ipRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	capacity   float64
	refillRate float64
}

func newIPRateLimiter(rps float64, burst int) *ipRateLimiter {
	cap := float64(burst)
	if cap < 1 {
		cap = 1
	}
	if rps <= 0 {
		rps = 1
	}
	rl := &ipRateLimiter{
		buckets:    make(map[string]*tokenBucket),
		capacity:   cap,
		refillRate: rps,
	}
	go rl.cleanup()
	return rl
}

// cleanup periodically removes buckets that have been idle longer than bucketIdleTTL.
func (l *ipRateLimiter) cleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		l.mu.Lock()
		for ip, b := range l.buckets {
			if now.Sub(b.last) > bucketIdleTTL {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipRateLimiter) Allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[ip]
	if b == nil {
		b = &tokenBucket{capacity: l.capacity, refillRate: l.refillRate}
		l.buckets[ip] = b
	}
	return b.allow(now)
}
