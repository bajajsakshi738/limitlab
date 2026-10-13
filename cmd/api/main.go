package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"limitlab/internal/api"
	"limitlab/internal/keys"
	"limitlab/internal/limiter"
	"limitlab/internal/plans"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check a running instance and exit (for Docker)")
	flag.Parse()

	addr := envOr("ADDR", ":8080")
	if *healthcheck {
		os.Exit(checkHealth(addr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(addr, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(addr string, log *slog.Logger) error {
	instance := envOr("INSTANCE_NAME", hostname())
	keysFile := envOr("KEYS_FILE", "keys.local.json")

	rules := plans.Defaults()
	for name, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
		if name != r.Name {
			return fmt.Errorf("plan %q points at rule named %q", name, r.Name)
		}
	}

	store, err := keys.LoadFile(keysFile)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s not found; create a key first with: go run ./cmd/keygen", keysFile)
	}
	if err != nil {
		return err
	}
	for p := range store.Plans() {
		if _, ok := rules[p]; !ok {
			return fmt.Errorf("%s: a key uses unknown plan %q", keysFile, p)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mem := limiter.NewMemory(time.Now)
	go mem.RunSweeper(ctx, 30*time.Second)

	s := &api.Server{
		Keys:     store,
		Plans:    rules,
		Limiter:  mem,
		Instance: instance,
		Log:      log,
	}
	srv := api.NewHTTPServer(addr, s.Handler())

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "instance", instance, "store", "memory")
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Docker waits 10s after SIGTERM before killing us, so finish in-flight
	// requests a bit sooner than that.
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// checkHealth exists because the distroless image has no curl or shell.
func checkHealth(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "api"
	}
	return h
}
