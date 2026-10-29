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

// tokenUsageCreateTablePreSessionColumns creates a token_usage table with the
// column layout from before the session attribution feature added the session
// columns ahead of the timing columns, as found in existing target databases.
const tokenUsageCreateTablePreSessionColumns = `
	CREATE TABLE IF NOT EXISTS "%s".token_usage (
		timestamp TIMESTAMP,
		response_id VARCHAR PRIMARY KEY,
		status VARCHAR,
		prompt_tokens BIGINT,
		completion_tokens BIGINT,
		total_tokens BIGINT,
		cached_tokens BIGINT,
		request_received_at TIMESTAMP,
		first_response_at TIMESTAMP,
		last_response_at TIMESTAMP,
		error_at TIMESTAMP,
		first_response_latency_ms BIGINT,
		total_response_latency_ms BIGINT,
		session_id VARCHAR,
		session_name VARCHAR
	);
`

// TestMotherDuckInsertMapsColumnsByNameAcrossSchemaDrift guards the token usage
// upload insert against drifted source and target token_usage schemas: the
// insert must map source columns to target columns by name rather than
// positionally, since a target created before the session attribution columns
// were added has the timing columns ahead of the session columns. A positional
// insert would shuffle values between columns and fail the whole upload with a
// cast error (e.g. a session UUID string cast into a timestamp column).
func TestMotherDuckInsertMapsColumnsByNameAcrossSchemaDrift(t *testing.T) {
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
	sessionID := "01a18871-05e2-766f-87f9-d3af86d1ed6e"
	if err := store.Record(&Usage{
		SessionID:    sessionID,
		SessionName:  "sess-drift",
		PromptTokens: 3, CompletionTokens: 8, TotalTokens: 11, PromptTokensDetails: PromptTokensDetails{CachedTokens: 2},
	}, "chatcmpl-drift", "ok", timing, latency); err != nil {
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
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(tokenUsageCreateTablePreSessionColumns, mdDatabase)); err != nil {
		t.Fatalf("CREATE TABLE IF NOT EXISTS failed: %v", err)
	}
	// the source table has the post-session-attribution layout, the target
	// table has the pre-session-attribution layout; the insert must still map
	// columns by name and succeed
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckInsert, mdDatabase, mdDatabase)); err != nil {
		t.Fatalf("motherDuckInsert() error = %v, want nil across drifted source and target schemas", err)
	}

	recs, err := store.Query(fmt.Sprintf("SELECT timestamp, session_id, session_name, request_received_at, first_response_at, last_response_at, error_at, prompt_tokens FROM \"%s\".token_usage;", mdDatabase))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("Query() len = %d, want 1", len(recs))
	}
	row := recs[0]
	if got := row["session_id"].(string); got != sessionID {
		t.Errorf("session_id = %q, want %q", got, sessionID)
	}
	if got := row["session_name"].(string); got != "sess-drift" {
		t.Errorf("session_name = %q, want %q", got, "sess-drift")
	}
	if got := row["request_received_at"].(time.Time); !got.Equal(timing.RequestReceivedAt) {
		t.Errorf("request_received_at = %v, want %v", got, timing.RequestReceivedAt)
	}
	if got := row["first_response_at"].(time.Time); !got.Equal(timing.FirstResponseAt) {
		t.Errorf("first_response_at = %v, want %v", got, timing.FirstResponseAt)
	}
	if got := row["last_response_at"].(time.Time); !got.Equal(timing.LastResponseAt) {
		t.Errorf("last_response_at = %v, want %v", got, timing.LastResponseAt)
	}
	if got := row["error_at"].(time.Time); !got.IsZero() {
		t.Errorf("error_at = %v, want zero", got)
	}
	if got := row["prompt_tokens"].(int64); got != 3 {
		t.Errorf("prompt_tokens = %d, want 3", got)
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
