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
	host          string
	port          int
	bin           string
	args          []string
	env           []string
	allowedHosts  []string
	logoPath      string
	bucket        string
	accessKey     string
	secretKey     string
	region        string
	flushInterval time.Duration
	maxPartBytes  int64
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
	env = append(env, "REC_PI_CONFIG="+envOr("REC_PI_CONFIG", `{"baseUrl":"http://127.0.0.1:11500/v1","contextWindow":262144,"maxTokens":32768}`))
	env = append(env, "REC_API_KEY="+envOr("REC_API_KEY", ""))

	return &config{
		host:          host,
		port:          port,
		bin:           bin,
		args:          args,
		env:           env,
		allowedHosts:  strings.Split(os.Getenv("PI_WEB_ALLOWED_HOSTS"), ","),
		logoPath:      envOr("PI_LOGO_PATH", "/opt/rec/logo-pixels.json"),
		bucket:        envOr("AWS_S3_BUCKET_NAME", envOr("WEB_AWS_S3_BUCKET_NAME", "")),
		accessKey:     envOr("AWS_ACCESS_KEY_ID", envOr("WEB_AWS_ACCESS_KEY_ID", "")),
		secretKey:     envOr("AWS_SECRET_ACCESS_KEY", envOr("WEB_AWS_SECRET_ACCESS_KEY", "")),
		region:        envOr("AWS_REGION", envOr("WEB_AWS_REGION", "us-east-1")),
		flushInterval: time.Duration(envIntOr("PI_WEB_S3_FLUSH_INTERVAL_SECONDS", 120)) * time.Second,
		maxPartBytes:  envInt64Or("PI_WEB_S3_MAX_PART_BYTES", defaultSessionMaxPartBytes),
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

func envInt64Or(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
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

	var server *Server
	var uploader *sessionUploader
	if cfg.bucket != "" && cfg.accessKey != "" && cfg.secretKey != "" {
		store, err := newS3Store(context.Background(), cfg.bucket, cfg.accessKey, cfg.secretKey, cfg.region)
		if err != nil {
			log.Fatalf("failed to create s3 store: %v", err)
		}
		uploader = newSessionUploader(store, cfg.flushInterval, cfg.maxPartBytes, nil)
	}
	server = NewServer(rpc, cfg.allowedHosts, cfg.logoPath, uploader)
	if uploader != nil {
		uploader.source = func(ctx context.Context) (*sessionRef, error) { return server.SessionRef(ctx) }
		uploader.Start(context.Background())
	}

	srv := &http.Server{
		Addr:    cfg.host + ":" + strconv.Itoa(cfg.port),
		Handler: server.Routes(),
	}

	// Shut the HTTP server down when the pi child process exits or the server is
	// shut down, and close out any in-flight session uploads first.
	shutdownHTTP := func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("http shutdown error: %v", err)
		}
	}
	closeSessions := func(ctx context.Context, endReason string) {
		if uploader == nil {
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := uploader.Close(ctx, endReason); err != nil {
			log.Printf("session upload close error: %v", err)
		}
	}

	// Shut the HTTP server down when the pi child process exits, and close out
	// any in-flight session uploads first. The goroutine owns the whole shutdown
	// sequence; main waits for it on the way out (see below) so the final
	// session flush and _closed close marker cannot be cut off mid-flush.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		err := rpc.Wait()
		if err != nil {
			log.Printf("pi rpc process exited: %v", err)
		}
		endReason := "closed"
		if err != nil {
			endReason = "crashed"
		}
		shutdownHTTP(context.Background())
		closeSessions(context.Background(), endReason)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("shutting down: %v", sig)
		shutdownHTTP(context.Background())
		closeSessions(context.Background(), "closed")
		if err := rpc.Stop(); err != nil {
			log.Printf("failed to stop pi rpc child: %v", err)
		}
		os.Exit(0)
	}()

	log.Printf("pi web interface listening on http://%s:%d", cfg.host, cfg.port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server error: %v", err)
	}
	// ListenAndServe returns as soon as Shutdown completes, which is *before* the
	// shutdown goroutine above has run the final session upload close. Wait for
	// that sequence to finish before exiting, or the final flush can be cut off
	// mid-upload and the sessions/<id>/_closed marker never gets written.
	<-shutdownDone
}
