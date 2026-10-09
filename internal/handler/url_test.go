package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/url-shortener/domain"
	"example.com/url-shortener/internal/service"
	"example.com/url-shortener/internal/storage"
)

type fakeStore struct {
	create func(string, func() (string, error)) (string, error)
	get    func(string) (domain.URLRecord, error)
}

func (f fakeStore) Create(rawURL string, generate func() (string, error)) (string, error) {
	return f.create(rawURL, generate)
}
func (f fakeStore) Get(code string) (domain.URLRecord, error) { return f.get(code) }

func request(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
	return recorder
}

func assertJSONError(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body %s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("error response is not JSON")
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error JSON: %v", err)
	}
	if len(body) != 1 || body["error"] == "" || body["error"] == nil {
		t.Fatalf("unexpected error body: %#v", body)
	}
}

func TestHTTPFlow(t *testing.T) {
	mux := NewURLHandler(service.NewURLService(storage.NewMemoryStorage()), "https://short.example///").Routes()
	var previous ShortenResponse
	var createdAt string
	for i := 0; i < 2; i++ {
		shortened := request(mux, "POST", "/api/shorten", `{"url":"https://go.dev/doc/"}`)
		if shortened.Code != http.StatusCreated {
			t.Fatalf("shorten: %d %s", shortened.Code, shortened.Body.String())
		}
		var link ShortenResponse
		if err := json.Unmarshal(shortened.Body.Bytes(), &link); err != nil {
			t.Fatal(err)
		}
		if (len(link.Code) < 6 || len(link.Code) > 8) || link.ShortURL != "https://short.example/"+link.Code {
			t.Fatalf("bad response: %#v", link)
		}
		if i > 0 && link != previous {
			t.Fatalf("duplicate changed link: %#v vs %#v", previous, link)
		}
		previous = link
		for _, method := range []string{"GET", "HEAD"} {
			redirected := request(mux, method, "/"+link.Code, "")
			if redirected.Code != 302 || redirected.Header().Get("Location") != "https://go.dev/doc/" {
				t.Fatalf("redirect: %d, %v", redirected.Code, redirected.Header())
			}
			if method == "HEAD" && redirected.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
		}
		metadata := request(mux, "GET", "/api/v1/links/"+link.Code, "")
		if metadata.Code != 200 {
			t.Fatalf("metadata: %d %s", metadata.Code, metadata.Body.String())
		}
		var record domain.URLRecord
		if err := json.Unmarshal(metadata.Body.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.URL != "https://go.dev/doc/" {
			t.Fatalf("URL = %q", record.URL)
		}
		if _, err := time.Parse(time.RFC3339, record.CreatedAt); err != nil {
			t.Fatal(err)
		}
		if i > 0 && record.CreatedAt != createdAt {
			t.Fatal("duplicate changed creation time")
		}
		createdAt = record.CreatedAt
	}
	assertJSONError(t, request(mux, "GET", "/unknown", ""), 404)
	assertJSONError(t, request(mux, "GET", "/api/v1/links/unknown", ""), 404)
	if response := request(mux, "GET", "/extra/path", ""); response.Code != 404 {
		t.Fatalf("multi-segment redirect accepted: %d", response.Code)
	}
}

func TestConcurrentHTTPShortening(t *testing.T) {
	mux := NewURLHandler(service.NewURLService(storage.NewMemoryStorage()), "https://short.example").Routes()
	const count = 32
	var wg sync.WaitGroup
	links := make(chan ShortenResponse, count)
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := request(mux, "POST", "/api/shorten", `{"url":"https://same.example/path"}`)
			var link ShortenResponse
			if response.Code != 201 {
				failures <- fmt.Errorf("shorten status %d", response.Code)
				return
			}
			if err := json.Unmarshal(response.Body.Bytes(), &link); err != nil {
				failures <- err
				return
			}
			metadata := request(mux, "GET", "/api/v1/links/"+link.Code, "")
			redirect := request(mux, "GET", "/"+link.Code, "")
			if metadata.Code != 200 || redirect.Code != 302 {
				failures <- fmt.Errorf("metadata=%d redirect=%d", metadata.Code, redirect.Code)
				return
			}
			links <- link
		}()
	}
	wg.Wait()
	close(links)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var first ShortenResponse
	seen := 0
	for link := range links {
		if seen == 0 {
			first = link
		} else if first != link {
			t.Errorf("concurrent duplicates differ: %#v vs %#v", first, link)
		}
		seen++
	}
	if seen != count {
		t.Fatalf("successful requests=%d, want %d", seen, count)
	}
}

func TestBadRequests(t *testing.T) {
	mux := NewURLHandler(service.NewURLService(storage.NewMemoryStorage()), "http://localhost:8080").Routes()
	for _, body := range []string{"", "{", "null", `{}`, `{"url":""}`, `{"url":12}`, `{"url":"/relative"}`, `{"url":"https:///path"}`, `{"url":"ftp://example.com"}`, `{"url":"https://example.com"} {}`, `{"url":"https://example.com"} garbage`} {
		t.Run(body, func(t *testing.T) { assertJSONError(t, request(mux, "POST", "/api/shorten", body), 400) })
	}
	for _, tc := range []struct{ method, path, allow string }{
		{"GET", "/api/shorten", "POST"}, {"POST", "/abc123", "GET, HEAD"}, {"POST", "/api/v1/links/abc123", "GET"},
	} {
		response := request(mux, tc.method, tc.path, "")
		assertJSONError(t, response, 405)
		if response.Header().Get("Allow") != tc.allow {
			t.Fatalf("Allow = %q", response.Header().Get("Allow"))
		}
	}
}

func TestFakeStoreErrorMapping(t *testing.T) {
	unexpected := errors.New("backend unavailable")
	for _, tc := range []struct {
		name, method, path, body string
		err                      error
		status                   int
	}{
		{"wrapped invalid", "POST", "/api/shorten", `{"url":"https://example.com"}`, fmt.Errorf("wrapped: %w", domain.ErrInvalidURL), 400},
		{"create failure", "POST", "/api/shorten", `{"url":"https://example.com"}`, unexpected, 500},
		{"wrapped missing redirect", "GET", "/abc123", "", fmt.Errorf("wrapped: %w", domain.ErrNotFound), 404},
		{"wrapped missing metadata", "GET", "/api/v1/links/abc123", "", fmt.Errorf("wrapped: %w", domain.ErrNotFound), 404},
		{"redirect failure", "GET", "/abc123", "", unexpected, 500},
		{"metadata failure", "GET", "/api/v1/links/abc123", "", unexpected, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fakeStore{
				create: func(string, func() (string, error)) (string, error) { return "", tc.err },
				get: func(code string) (domain.URLRecord, error) {
					if code != "abc123" {
						t.Errorf("extracted code = %q", code)
					}
					return domain.URLRecord{}, tc.err
				},
			}
			response := request(NewURLHandler(service.NewURLService(store), "https://short.example").Routes(), tc.method, tc.path, tc.body)
			assertJSONError(t, response, tc.status)
			if response.Header().Get("Location") != "" {
				t.Fatal("error response redirected")
			}
			if strings.Contains(response.Body.String(), unexpected.Error()) {
				t.Fatal("backend error leaked")
			}
		})
	}
}

type brokenWriter struct {
	header         http.Header
	status, writes int
}

func (w *brokenWriter) Header() http.Header       { return w.header }
func (w *brokenWriter) WriteHeader(status int)    { w.status = status; w.writes++ }
func (w *brokenWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }

func TestWriteFailureDoesNotWriteSecondStatus(t *testing.T) {
	writer := &brokenWriter{header: make(http.Header)}
	writeJSON(writer, 201, ShortenResponse{Code: "abc123"})
	if writer.status != 201 || writer.writes != 1 {
		t.Fatalf("status writes = %d, status = %d", writer.writes, writer.status)
	}
}

func TestDurableHTTPRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	first, err := storage.OpenFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewURLHandler(service.NewURLService(first), "https://short.example").Routes()
	shortened := request(mux, "POST", "/api/shorten", `{"url":"https://persist.example/path"}`)
	if shortened.Code != 201 {
		t.Fatalf("create=%d %s", shortened.Code, shortened.Body.String())
	}
	var link ShortenResponse
	if err := json.Unmarshal(shortened.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	before := request(mux, "GET", "/api/v1/links/"+link.Code, "")
	second, err := storage.OpenFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewURLHandler(service.NewURLService(second), "https://short.example").Routes()
	after := request(reopened, "GET", "/api/v1/links/"+link.Code, "")
	if after.Code != 200 || before.Body.String() != after.Body.String() {
		t.Fatal("metadata changed after restart")
	}
	duplicate := request(reopened, "POST", "/api/shorten", `{"url":"https://persist.example/path"}`)
	if duplicate.Code != 201 || shortened.Body.String() != duplicate.Body.String() {
		t.Fatal("duplicate changed after restart")
	}
	redirect := request(reopened, "GET", "/"+link.Code, "")
	if redirect.Code != 302 || redirect.Header().Get("Location") != "https://persist.example/path" {
		t.Fatal("restart redirect failed")
	}
}

func TestPersistenceFailureReturns500(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store, err := storage.OpenFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	// Turn the destination into a directory to force a real failed rename.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	mux := NewURLHandler(service.NewURLService(store), "https://short.example").Routes()
	response := request(mux, "POST", "/api/shorten", `{"url":"https://example.com"}`)
	assertJSONError(t, response, 500)
}
