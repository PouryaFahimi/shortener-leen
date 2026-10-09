package handler

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type tokenBucket struct {
	lock                sync.Mutex
	rate, burst, tokens float64
	last                time.Time
	now                 func() time.Time
}

func newTokenBucket(rate float64, burst int) *tokenBucket {
	now := time.Now()
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now, now: time.Now}
}

func (b *tokenBucket) allow() (bool, int) {
	b.lock.Lock()
	defer b.lock.Unlock()
	now := b.now()
	if now.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, max(1, int(math.Ceil((1-b.tokens)/b.rate)))
}

// LimitCreates applies one bounded-memory, process-wide bucket. It does not
// trust proxy headers or penalize redirects and metadata requests.
func LimitCreates(next http.Handler, rate float64, burst int) http.Handler {
	if rate == 0 {
		return next
	}
	bucket := newTokenBucket(rate, burst)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/shorten" {
			if allowed, retry := bucket.allow(); !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeError(w, http.StatusTooManyRequests, "create rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
