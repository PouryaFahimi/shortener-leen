package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/url-shortener/internal/storage"
)

func TestParseConfig(t *testing.T) {
	defaults := config{addr: ":8080", base: "http://localhost:8080", store: "memory", data: "data/links.json", createRate: 5, createBurst: 20, shutdownTimeout: 10 * time.Second}
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil || cfg != defaults {
		t.Fatalf("defaults=%v err=%v", cfg, err)
	}
	cfg, err = parseConfig([]string{"-addr", "127.0.0.1:9090", "-base", "https://short.example/", "-store", "file", "-data", "links.json", "-create-rate", "2", "-create-burst", "3", "-shutdown-timeout", "5s"}, io.Discard)
	want := config{addr: "127.0.0.1:9090", base: "https://short.example/", store: "file", data: "links.json", createRate: 2, createBurst: 3, shutdownTimeout: 5 * time.Second}
	if err != nil || cfg != want {
		t.Fatalf("custom=%v err=%v", cfg, err)
	}
	for _, args := range [][]string{{"-unknown"}, {"extra"}, {"-store", "bad"}, {"-store", "file", "-data", ""}, {"-create-rate", "-1"}, {"-create-rate", "NaN"}, {"-create-rate", "+Inf"}, {"-create-burst", "0"}, {"-shutdown-timeout", "0s"}} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
	if _, err := parseConfig([]string{"-help"}, io.Discard); err != flag.ErrHelp {
		t.Fatalf("help=%v", err)
	}
}

func TestBackendSelection(t *testing.T) {
	if store, err := openStore(config{store: "memory"}); err != nil || store == nil {
		t.Fatalf("memory=%v %v", store, err)
	}
	path := filepath.Join(t.TempDir(), "links.json")
	if store, err := openStore(config{store: "file", data: path}); err != nil || store == nil {
		t.Fatalf("file=%v %v", store, err)
	}
	if _, err := openStore(config{store: "bad"}); err == nil {
		t.Fatal("unknown backend accepted")
	}
	if err := os.WriteFile(path, []byte("bad JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"-store", "file", "-data", path}, io.Discard, io.Discard); err == nil {
		t.Fatal("corrupt store startup accepted")
	}
}

func TestServerConfigurationAndRateLimit(t *testing.T) {
	server := newServer(config{addr: ":9090", base: "https://short.example/", createRate: 0.001, createBurst: 1}, storage.NewMemoryStorage())
	if server.Addr != ":9090" || server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 10*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("server=%#v", server)
	}
	for i, expected := range []int{201, 429} {
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/shorten", strings.NewReader(`{"url":"https://example.com"}`)))
		if response.Code != expected {
			t.Fatalf("request %d=%d %s", i, response.Code, response.Body.String())
		}
	}
}

func listenerForTest(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server")
	}
}

func TestGracefulShutdownDrainsInflight(t *testing.T) {
	listener := listenerForTest(t)
	started, release, draining := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(201)
		io.WriteString(w, "committed")
	})}
	server.RegisterOnShutdown(func() { close(draining) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, server, listener, 3*time.Second) }()
	response := make(chan error, 1)
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		result, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- err
			return
		}
		defer result.Body.Close()
		body, err := io.ReadAll(result.Body)
		if err == nil && (result.StatusCode != 201 || string(body) != "committed") {
			err = errors.New("in-flight response lost")
		}
		response <- err
	}()
	waitSignal(t, started)
	cancel()
	waitSignal(t, draining)
	select {
	case err := <-served:
		t.Fatalf("shutdown did not drain: %v", err)
	default:
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestGracefulShutdownDeadline(t *testing.T) {
	listener := listenerForTest(t)
	started := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, server, listener, 20*time.Millisecond) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 5 * time.Second}
		result, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			result.Body.Close()
		}
	}()
	waitSignal(t, started)
	cancel()
	if err := <-served; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	waitSignal(t, clientDone)
}

func TestServeAndRunFailures(t *testing.T) {
	listener := listenerForTest(t)
	listener.Close()
	if err := serve(context.Background(), &http.Server{}, listener, time.Second); err == nil {
		t.Fatal("closed listener accepted")
	}
	if err := run(context.Background(), []string{"-store", "bad"}, io.Discard, io.Discard); err == nil {
		t.Fatal("bad flags accepted")
	}
	if err := run(context.Background(), []string{"-addr", "invalid-address"}, io.Discard, io.Discard); err == nil {
		t.Fatal("bad listen address accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"-addr", "127.0.0.1:0"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("cancelled startup=%v", err)
	}
}
