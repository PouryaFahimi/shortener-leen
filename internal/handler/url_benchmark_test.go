package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/url-shortener/internal/service"
	"example.com/url-shortener/internal/storage"
)

func BenchmarkShorten(b *testing.B) {
	b.Run("Duplicate", func(b *testing.B) {
		s := service.NewURLService(storage.NewMemoryStorage())
		code, err := s.Shorten("https://example.com/duplicate")
		if err != nil {
			b.Fatal(err)
		}
		mux := NewURLHandler(s, "https://short.example").Routes()
		expected, err := json.Marshal(ShortenResponse{Code: code, ShortURL: "https://short.example/" + code})
		if err != nil {
			b.Fatal(err)
		}
		expectedBody := string(expected) + "\n"
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/shorten", strings.NewReader(`{"url":"https://example.com/duplicate"}`)))
			if response.Code != 201 || response.Body.String() != expectedBody {
				b.Fatalf("duplicate response=%d %s", response.Code, response.Body.String())
			}
		}
	})
	b.Run("NewURL", func(b *testing.B) {
		const batchSize = 1024
		bodies := make([]string, batchSize)
		for i := range bodies {
			bodies[i] = fmt.Sprintf(`{"url":"https://example.com/new/%d"}`, i)
		}
		mux := NewURLHandler(service.NewURLService(storage.NewMemoryStorage()), "https://short.example").Routes()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if i > 0 && i%batchSize == 0 {
				b.StopTimer()
				mux = NewURLHandler(service.NewURLService(storage.NewMemoryStorage()), "https://short.example").Routes()
				b.StartTimer()
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/shorten", strings.NewReader(bodies[i%batchSize])))
			if response.Code != 201 {
				b.Fatalf("new URL response=%d %s", response.Code, response.Body.String())
			}
		}
	})
}

func BenchmarkRedirect(b *testing.B) {
	s := service.NewURLService(storage.NewMemoryStorage())
	code, err := s.Shorten("https://example.com/redirect")
	if err != nil {
		b.Fatal(err)
	}
	mux := NewURLHandler(s, "https://short.example").Routes()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("GET", "/"+code, nil))
		if response.Code != 302 || response.Header().Get("Location") != "https://example.com/redirect" {
			b.Fatalf("redirect response=%d %v", response.Code, response.Header())
		}
	}
}
