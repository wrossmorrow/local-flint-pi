package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
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
	if err := proc.uploadToMotherDuck(context.Background(), "/tmp/usage.duckdb"); err != nil {
		t.Fatalf("uploadToMotherDuck() error = %v, want nil when no motherduck database is configured", err)
	}
}

func TestMotherDuckInsertSkipsExistingResponseIDs(t *testing.T) {
	t.Parallel()

	mdDatabase := "testdb"
	store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	defer store.Close()

	timing := ResponseTiming{
		RequestReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FirstResponseAt:   time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
		LastResponseAt:    time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC),
	}
	latency := timing.Latency()
	if err := store.Record(&Usage{PromptTokens: 3, CompletionTokens: 8, TotalTokens: 11}, "chatcmpl-existing", "ok", timing, latency); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	ctx := context.Background()
	conn, err := store.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	defer conn.Close()

	targetPath := filepath.Join(t.TempDir(), "target.duckdb")
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("ATTACH '%s' AS \"%s\";", targetPath, mdDatabase)); err != nil {
		t.Fatalf("ATTACH() error = %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\".token_usage AS SELECT * FROM token_usage WHERE 1=0;", mdDatabase)); err != nil {
		t.Fatalf("CREATE TABLE IF NOT EXISTS failed: %v", err)
	}
	// seed the target with the row already uploaded from the local usage table
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO \"%s\".token_usage SELECT * FROM token_usage;", mdDatabase)); err != nil {
		t.Fatalf("seed INSERT failed: %v", err)
	}

	// a second usage row arrives locally after the upload, so the insert must
	// add only the row that is not yet present in the target
	if err := store.Record(&Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, "chatcmpl-new", "ok", timing, latency); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckInsert, mdDatabase, mdDatabase)); err != nil {
		t.Fatalf("motherDuckInsert() error = %v", err)
	}

	recs, err := store.Query(fmt.Sprintf("SELECT count(*) AS count FROM \"%s\".token_usage;", mdDatabase))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("Query() len = %d, want 1", len(recs))
	}
	got, ok := recs[0]["count"].(int64)
	if !ok {
		t.Fatalf("Query()[0][\"count\"] = %#v, want int64", recs[0]["count"])
	}
	// the pre-existing row is not re-inserted, the new row is
	if got != 2 {
		t.Errorf("count of rows in %q token_usage = %d, want 2", mdDatabase, got)
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
