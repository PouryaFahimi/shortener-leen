package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/url-shortener/domain"
	"example.com/url-shortener/internal/handler"
	"example.com/url-shortener/internal/service"
	"example.com/url-shortener/internal/storage"
)

type Store interface{ domain.Store }

type config struct {
	addr, base, store, data string
	createRate              float64
	createBurst             int
	shutdownTimeout         time.Duration
}

func parseConfig(args []string, output io.Writer) (config, error) {
	flags := flag.NewFlagSet("url-shortener", flag.ContinueOnError)
	flags.SetOutput(output)
	var cfg config
	flags.StringVar(&cfg.addr, "addr", ":8080", "HTTP listen address")
	flags.StringVar(&cfg.base, "base", "http://localhost:8080", "base address for short URLs")
	flags.StringVar(&cfg.store, "store", "memory", "storage backend: memory or file")
	flags.StringVar(&cfg.data, "data", "data/links.json", "JSON storage path for -store file")
	flags.Float64Var(&cfg.createRate, "create-rate", 5, "create requests per second per process; 0 disables")
	flags.IntVar(&cfg.createBurst, "create-burst", 20, "maximum burst of create requests")
	flags.DurationVar(&cfg.shutdownTimeout, "shutdown-timeout", 10*time.Second, "maximum graceful drain time")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("unexpected positional arguments")
	}
	if cfg.store != "memory" && cfg.store != "file" {
		return config{}, errors.New("-store must be memory or file")
	}
	if cfg.store == "file" && cfg.data == "" {
		return config{}, errors.New("-data must not be empty")
	}
	if cfg.createRate < 0 || math.IsNaN(cfg.createRate) || math.IsInf(cfg.createRate, 0) || cfg.createBurst < 1 {
		return config{}, errors.New("-create-rate must be finite and nonnegative; -create-burst must be positive")
	}
	if cfg.shutdownTimeout <= 0 {
		return config{}, errors.New("-shutdown-timeout must be positive")
	}
	return cfg, nil
}

func openStore(cfg config) (Store, error) {
	switch cfg.store {
	case "memory":
		return storage.NewMemoryStorage(), nil
	case "file":
		return storage.OpenFileStorage(cfg.data)
	default:
		return nil, errors.New("unknown storage backend")
	}
}

func newServer(cfg config, store Store) *http.Server {
	urlHandler := handler.NewURLHandler(service.NewURLService(store), cfg.base)
	return &http.Server{
		Addr:              cfg.addr,
		Handler:           handler.LimitCreates(urlHandler.Routes(), cfg.createRate, cfg.createBurst),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := server.Shutdown(drain); err != nil {
			server.Close()
			<-served
			return fmt.Errorf("shutdown: %w", err)
		}
		err := <-served
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve during shutdown: %w", err)
		}
		return nil
	}
}

func run(ctx context.Context, args []string, output, errorOutput io.Writer) error {
	cfg, err := parseConfig(args, errorOutput)
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return fmt.Errorf("initialize store: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	fmt.Fprintln(output, "Server listening on", listener.Addr())
	return serve(ctx, newServer(cfg, store), listener, cfg.shutdownTimeout)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}
