package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func newTestProcessor(t *testing.T, mdDatabase string) *tokenCounterRequestProcessor {
	t.Helper()
	store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &tokenCounterRequestProcessor{
		logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})),
		store:      store,
		mdDatabase: mdDatabase,
	}
}

func TestUploadToMotherDuckSkippedWhenUnconfigured(t *testing.T) {
	t.Parallel()

	proc := newTestProcessor(t, "")
	if err := proc.uploadToMotherDuck(context.Background()); err != nil {
		t.Fatalf("uploadToMotherDuck() error = %v, want nil when no motherduck database is configured", err)
	}
}

func TestMotherDuckDatabaseFromEnv(t *testing.T) {
	t.Setenv(motherDuckDatabaseEnv, "")
	if got := MotherDuckDatabase(); got != "" {
		t.Errorf("MotherDuckDatabase() = %q, want empty when %s is unset", got, motherDuckDatabaseEnv)
	}
	t.Setenv(motherDuckDatabaseEnv, "some_database")
	if got := MotherDuckDatabase(); got != "some_database" {
		t.Errorf("MotherDuckDatabase() = %q, want some_database", got)
	}
}
