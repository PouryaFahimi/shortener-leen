package handler

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenBucketRefill(t *testing.T) {
	bucket := newTokenBucket(2, 2)
	now := time.Unix(100, 0)
	bucket.last = now
	bucket.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if allowed, _ := bucket.allow(); !allowed {
			t.Fatal("burst rejected")
		}
	}
	if allowed, retry := bucket.allow(); allowed || retry != 1 {
		t.Fatalf("exhausted: %v %d", allowed, retry)
	}
	now = now.Add(250 * time.Millisecond)
	if allowed, _ := bucket.allow(); allowed {
		t.Fatal("partial token allowed")
	}
	now = now.Add(250 * time.Millisecond)
	if allowed, _ := bucket.allow(); !allowed {
		t.Fatal("refill rejected")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 2; i++ {
		if allowed, _ := bucket.allow(); !allowed {
			t.Fatal("burst refill failed")
		}
	}
	if allowed, _ := bucket.allow(); allowed {
		t.Fatal("refill exceeded burst")
	}
}

func TestRateLimitScopeAndDisabled(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	limited := LimitCreates(next, 0.001, 1)
	if response := request(limited, "POST", "/api/shorten", "{}"); response.Code != 201 {
		t.Fatal(response.Code)
	}
	response := request(limited, "POST", "/api/shorten", "{}")
	assertJSONError(t, response, 429)
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	for _, path := range []string{"/abc123", "/api/v1/links/abc123", "/api/shorten"} {
		if response := request(limited, "GET", path, ""); response.Code != 201 {
			t.Fatalf("non-create was limited: %s", path)
		}
	}
	disabled := LimitCreates(next, 0, 1)
	for i := 0; i < 10; i++ {
		if response := request(disabled, "POST", "/api/shorten", "{}"); response.Code != 201 {
			t.Fatal(response.Code)
		}
	}
}

func TestConcurrentRateLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	limited := LimitCreates(next, 0.000001, 5)
	var accepted atomic.Int32
	var rejected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := request(limited, "POST", "/api/shorten", "{}")
			if response.Code == 201 {
				accepted.Add(1)
			} else if response.Code == 429 {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 5 || rejected.Load() != 35 {
		t.Fatalf("accepted=%d rejected=%d", accepted.Load(), rejected.Load())
	}
}
