package relaycomm

import (
	"sync"
	"time"
)

const (
	rateBurst        = 20 // Max tokens (burst allowance per device)
	rateRefill       = 10 // Tokens added per second per device
	rateGlobalBurst  = 50 // Max tokens (burst allowance across all devices)
	rateGlobalRefill = 25 // Tokens added per second globally
	bucketExpiry     = time.Hour
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) count(refill, burst int) bool {
	now := time.Now()
	b.tokens = min(float64(burst), b.tokens+now.Sub(b.last).Seconds()*float64(refill))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type rateLimiter struct {
	mu           sync.Mutex
	buckets      map[string]*tokenBucket
	globalBucket tokenBucket
	lastCleanup  time.Time
}

func newRateLimiter() rateLimiter {
	return rateLimiter{
		buckets:      make(map[string]*tokenBucket),
		globalBucket: tokenBucket{tokens: float64(rateGlobalBurst), last: time.Now()},
		lastCleanup:  time.Now(),
	}
}

func (rl *rateLimiter) allow(deviceID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Evict idle buckets to prevent unbounded map growth
	now := time.Now()
	if now.Sub(rl.lastCleanup) > bucketExpiry {
		for id, b := range rl.buckets {
			if now.Sub(b.last) > bucketExpiry {
				delete(rl.buckets, id)
			}
		}
		rl.lastCleanup = now
	}

	// Check per-device bucket first so a flooding device doesn't starve the global budget
	b, ok := rl.buckets[deviceID]
	if !ok {
		b = &tokenBucket{tokens: float64(rateBurst), last: time.Now()}
		rl.buckets[deviceID] = b
	}
	if !b.count(rateRefill, rateBurst) {
		return false
	}
	if !rl.globalBucket.count(rateGlobalRefill, rateGlobalBurst) {
		return false
	}
	return true
}