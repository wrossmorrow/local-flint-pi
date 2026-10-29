package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// config holds the runtime configuration for the pi web interface.
type config struct {
	host         string
	port         int
	bin          string
	args         []string
	env          []string
	allowedHosts []string
	logoPath     string
}

// loadConfig reads the web interface configuration from the environment.
func loadConfig() (*config, error) {
	host := envOr("PI_WEB_HOST", "127.0.0.1")
	port := envIntOr("PI_WEB_PORT", 8787)
	bin := envOr("PI_BIN", "pi")

	// PI_ARGS replaces the default pi arguments (whitespace-separated); used to
	// point the web server at testdata/fake-pi.mjs during UI development.
	args := []string{
		"--mode", "rpc",
		"--extension", "/opt/rec/flint-pi.ts",
		"--provider", "rec",
		"--model", "flint",
	}
	if v := os.Getenv("PI_ARGS"); v != "" {
		args = strings.Fields(v)
	}
	if name := envOr("SESSION_NAME", ""); name != "" {
		args = append(args, "--name", name)
	}

	env := os.Environ()
	env = append(env, "REC_PI_CONFIG="+envOr("REC_PI_CONFIG", `{"baseUrl":"http://127.0.0.1:11005/v1","contextWindow":262144,"maxTokens":32768}`))
	env = append(env, "REC_API_KEY="+envOr("REC_API_KEY", ""))

	return &config{
		host:         host,
		port:         port,
		bin:          bin,
		args:         args,
		env:          env,
		allowedHosts: strings.Split(os.Getenv("PI_WEB_ALLOWED_HOSTS"), ","),
		logoPath:     envOr("PI_LOGO_PATH", "/opt/rec/logo-pixels.json"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	rpc := NewRpcClient(cfg.bin, cfg.args, cfg.env)
	if err := rpc.Start(); err != nil {
		log.Fatalf("failed to start pi rpc client: %v", err)
	}

	server := NewServer(rpc, cfg.allowedHosts, cfg.logoPath)

	srv := &http.Server{
		Addr:    cfg.host + ":" + strconv.Itoa(cfg.port),
		Handler: server.Routes(),
	}

	// Shut the HTTP server down when the pi child process exits or the server is
	// shut down.
	go func() {
		if err := rpc.Wait(); err != nil {
			log.Printf("pi rpc process exited: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("http shutdown error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("shutting down: %v", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("http shutdown error: %v", err)
		}
		if err := rpc.Stop(); err != nil {
			log.Printf("failed to stop pi rpc child: %v", err)
		}
		os.Exit(0)
	}()

	log.Printf("pi web interface listening on http://%s:%d", cfg.host, cfg.port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server error: %v", err)
	}
}
