package storage

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"example.com/url-shortener/domain"
)

func fixed(code string) func() (string, error) { return func() (string, error) { return code, nil } }

func TestCreateLookupAndDuplicate(t *testing.T) {
	s := NewMemoryStorage()
	code, err := s.Create("https://example.com", fixed("abc123"))
	if err != nil || code != "abc123" {
		t.Fatalf("code=%q err=%v", code, err)
	}
	before, err := s.Get(code)
	if err != nil || before.URL != "https://example.com" {
		t.Fatalf("record=%v err=%v", before, err)
	}
	if _, err := time.Parse(time.RFC3339, before.CreatedAt); err != nil {
		t.Fatal(err)
	}
	// Give the saved record an older timestamp so a duplicate that accidentally
	// updates created_at cannot pass merely because both calls share one second.
	before.CreatedAt = "2000-01-01T00:00:00Z"
	s.urls[code] = before
	code, err = s.Create(before.URL, func() (string, error) { t.Fatal("duplicate generated a code"); return "", nil })
	after, getErr := s.Get(code)
	if err != nil || getErr != nil || after != before {
		t.Fatalf("duplicate changed metadata: %v %v %v", after, err, getErr)
	}
	if _, err := s.Get("unknown"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing error: %v", err)
	}
}

func TestCollisionsAndGeneratorErrors(t *testing.T) {
	s := NewMemoryStorage()
	if _, err := s.Create("https://first.example", fixed("abc123")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	code, err := s.Create("https://second.example", func() (string, error) {
		calls++
		if calls == 1 {
			return "abc123", nil
		}
		return "def456", nil
	})
	if err != nil || code != "def456" || calls != 2 {
		t.Fatalf("retry code=%q calls=%d err=%v", code, calls, err)
	}
	calls = 0
	if _, err := s.Create("https://third.example", func() (string, error) { calls++; return "abc123", nil }); !errors.Is(err, domain.ErrCodeCollision) || calls != maxCodeAttempts {
		t.Fatalf("exhaustion calls=%d err=%v", calls, err)
	}
	failure := errors.New("random source failure")
	if _, err := s.Create("https://failed.example", func() (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatalf("generator error not wrapped: %v", err)
	}
	if len(s.urls) != 2 || len(s.codesByURL) != 2 {
		t.Fatal("failed creates changed indexes")
	}
	record, err := s.Get("abc123")
	if err != nil || record.URL != "https://first.example" {
		t.Fatal("collision overwrote original")
	}
}

func TestConcurrentCreateAndRead(t *testing.T) {
	s := NewMemoryStorage()
	var wg sync.WaitGroup
	failures := make(chan error, 80)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, err := s.Create("https://same.example", fixed("same00"))
			if err != nil || code != "same00" {
				failures <- fmt.Errorf("duplicate code=%q err=%v", code, err)
				return
			}
			raw := fmt.Sprintf("https://example.com/%d", i)
			unique, err := s.Create(raw, fixed(fmt.Sprintf("%06d", i)))
			if err != nil {
				failures <- err
				return
			}
			for j := 0; j < 20; j++ {
				same, err := s.Get(code)
				if err != nil || same.URL != "https://same.example" {
					failures <- fmt.Errorf("bad shared lookup: %v", err)
					return
				}
				record, err := s.Get(unique)
				if err != nil || record.URL != raw {
					failures <- fmt.Errorf("bad distinct lookup: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if len(s.urls) != 41 || len(s.codesByURL) != 41 {
		t.Fatalf("index sizes=%d,%d", len(s.urls), len(s.codesByURL))
	}
}
