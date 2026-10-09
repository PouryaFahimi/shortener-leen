package service

import (
	"errors"
	"fmt"
	"regexp"
	"testing"

	"example.com/url-shortener/domain"
	"example.com/url-shortener/internal/storage"
)

func TestURLValidation(t *testing.T) {
	s := NewURLService(storage.NewMemoryStorage())
	for _, raw := range []string{"", "not a url", "/relative", "https:///path", "ftp://example.com", "http://", " https://example.com", "https://example.com/%zz"} {
		t.Run(raw, func(t *testing.T) {
			if code, err := s.Shorten(raw); code != "" || !errors.Is(err, domain.ErrInvalidURL) {
				t.Fatalf("code=%q err=%v", code, err)
			}
		})
	}
	for _, raw := range []string{"http://example.com", "https://example.com/path?q=1#fragment"} {
		code, err := s.Shorten(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[a-zA-Z0-9]{6,8}$`).MatchString(code) {
			t.Fatalf("invalid code %q", code)
		}
		if target, err := s.Resolve(code); err != nil || target != raw {
			t.Fatalf("target=%q err=%v", target, err)
		}
		if record, err := s.MetaData(code); err != nil || record.URL != raw {
			t.Fatalf("record=%v err=%v", record, err)
		}
	}
}

func TestExactURLIdempotency(t *testing.T) {
	s := NewURLService(storage.NewMemoryStorage())
	first, err := s.Shorten("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Shorten("https://example.com")
	if err != nil || again != first {
		t.Fatalf("duplicate=%q first=%q err=%v", again, first, err)
	}
	different, err := s.Shorten("https://example.com/")
	if err != nil || different == first {
		t.Fatalf("trailing slash incorrectly normalized: %q, %v", different, err)
	}
}

type failingStore struct{ err error }

func (f failingStore) Create(string, func() (string, error)) (string, error) { return "", f.err }
func (f failingStore) Get(string) (domain.URLRecord, error)                  { return domain.URLRecord{}, f.err }

func TestErrorWrapping(t *testing.T) {
	for _, underlying := range []error{domain.ErrNotFound, errors.New("storage failure")} {
		s := NewURLService(failingStore{err: underlying})
		if _, err := s.Shorten("https://example.com"); !errors.Is(err, underlying) {
			t.Fatalf("shorten did not wrap: %v", err)
		}
		if _, err := s.Resolve("abc123"); !errors.Is(err, underlying) {
			t.Fatalf("resolve did not wrap: %v", err)
		}
		if _, err := s.MetaData("abc123"); !errors.Is(err, underlying) {
			t.Fatalf("metadata did not wrap: %v", err)
		}
	}
}

func TestGenerateCode(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-zA-Z0-9]{6,8}$`)
	for i := 0; i < 50; i++ {
		code, err := generateCode()
		if err != nil || !pattern.MatchString(code) {
			t.Fatal(fmt.Sprintf("code=%q err=%v", code, err))
		}
	}
}
