package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

// UsageRecord is a timestamped token-usage observation as stored in the
// tabular usage database.
type UsageRecord struct {
	Timestamp   time.Time       `json:"timestamp"`
	ResponseID  string          `json:"response_id,omitempty"`
	Status      string          `json:"status,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	SessionName string          `json:"session_name,omitempty"`
	Tokens      TokenUsage      `json:"tokens"`
	Timing      ResponseTiming  `json:"timing"`
	Latency     ResponseLatency `json:"latency"`
}

// TokenUsage is the token accounting nested under the "tokens" key of a usage
// record.
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt"`
	CompletionTokens int64 `json:"completion"`
	TotalTokens      int64 `json:"total"`
	CachedTokens     int64 `json:"cached"`
}

// tokenUsageColumn describes one expected column of the token_usage table, in
// tabular column order, with its DuckDB type and default value. Columns that are
// missing from an existing usage database are added by migrateTokenUsageSchema.
type tokenUsageColumn struct {
	Name    string
	Type    string
	Default string
}

// tokenUsageColumns lists the expected columns of the token_usage table, in
// tabular column order, with their DuckDB types and defaults for columns that
// may be added later to an existing usage database.
var tokenUsageColumns = []tokenUsageColumn{
	{Name: "timestamp", Type: "TIMESTAMP"},
	{Name: "response_id", Type: "VARCHAR"},
	{Name: "status", Type: "VARCHAR"},
	{Name: "prompt_tokens", Type: "BIGINT", Default: "0"},
	{Name: "completion_tokens", Type: "BIGINT", Default: "0"},
	{Name: "total_tokens", Type: "BIGINT", Default: "0"},
	{Name: "cached_tokens", Type: "BIGINT", Default: "0"},
	{Name: "session_id", Type: "VARCHAR", Default: "''"},
	{Name: "session_name", Type: "VARCHAR", Default: "''"},
	{Name: "request_received_at", Type: "TIMESTAMP"},
	{Name: "first_response_at", Type: "TIMESTAMP"},
	{Name: "last_response_at", Type: "TIMESTAMP"},
	{Name: "error_at", Type: "TIMESTAMP"},
	{Name: "first_response_latency_ms", Type: "BIGINT", Default: "0"},
	{Name: "total_response_latency_ms", Type: "BIGINT", Default: "0"},
}

// migrateTokenUsageSchema adds token_usage columns that are missing from an
// existing usage database, since CREATE TABLE IF NOT EXISTS does not add columns
// to tables that already exist. Missing columns are added with their defaults so
// existing rows remain readable. It is a no-op for a freshly created table.
func migrateTokenUsageSchema(s *UsageStore) error {
	rows, err := s.conn.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = 'token_usage'`)
	if err != nil {
		return fmt.Errorf("failed to query token_usage columns: %w", err)
	}
	defer rows.Close()

	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("failed to scan token_usage column name: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate token_usage columns: %w", err)
	}

	for _, c := range tokenUsageColumns {
		if have[c.Name] {
			continue
		}
		ddl := fmt.Sprintf(`ALTER TABLE token_usage ADD COLUMN "%s" %s`, c.Name, c.Type)
		if c.Default != "" {
			ddl += fmt.Sprintf(" DEFAULT %s", c.Default)
		}
		if _, err := s.conn.Exec(ddl); err != nil {
			return fmt.Errorf("failed to add missing token_usage column %q: %w", c.Name, err)
		}
	}
	return nil
}

// tokenUsageRow mirrors a single row of the token_usage table as scanned out
// of the database, in tabular column order.
type tokenUsageRow struct {
	Timestamp              time.Time
	ResponseID             string
	Status                 string
	PromptTokens           int64
	CompletionTokens       int64
	TotalTokens            int64
	CachedTokens           int64
	SessionID              string
	SessionName            string
	RequestReceivedAt      time.Time
	FirstResponseAt        time.Time
	LastResponseAt         time.Time
	ErrorAt                time.Time
	FirstResponseLatencyMs int64
	TotalResponseLatencyMs int64
}

// UsageStore appends token usage records to a DuckDB database file. It is
// safe for concurrent use and holds a connection pool open for writes and reads.
type UsageStore struct {
	conn   *sql.DB
	dbPath string
}

// NewUsageStore opens (creating if necessary) the DuckDB usage database at
// dbPath and ensures the token_usage table exists.
func NewUsageStore(dbPath string) (*UsageStore, error) {
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS token_usage (
		timestamp TIMESTAMP,
		response_id VARCHAR,
		status VARCHAR,
		prompt_tokens BIGINT,
		completion_tokens BIGINT,
		total_tokens BIGINT,
		cached_tokens BIGINT,
		session_id VARCHAR,
		session_name VARCHAR,
		request_received_at TIMESTAMP,
		first_response_at TIMESTAMP,
		last_response_at TIMESTAMP,
		error_at TIMESTAMP,
		first_response_latency_ms BIGINT,
		total_response_latency_ms BIGINT
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateTokenUsageSchema(&UsageStore{conn: db, dbPath: dbPath}); err != nil {
		db.Close()
		return nil, err
	}
	return &UsageStore{conn: db, dbPath: dbPath}, nil
}

// Record appends a token usage record to the usage database.
func (s *UsageStore) Record(u *Usage, responseID string, status string, timing ResponseTiming, latency ResponseLatency) error {
	if u == nil {
		return errors.New("cannot record usage from nil usage")
	}
	ctx := context.Background()
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO token_usage (
			timestamp, response_id, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens, session_id, session_name,
			request_received_at, first_response_at, last_response_at, error_at, first_response_latency_ms, total_response_latency_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		time.Now().UTC(),
		responseID,
		status,
		u.PromptTokens,
		u.CompletionTokens,
		u.TotalTokens,
		u.PromptTokensDetails.CachedTokens,
		u.SessionID,
		u.SessionName,
		timing.RequestReceivedAt,
		timing.FirstResponseAt,
		timing.LastResponseAt,
		timing.ErrorAt,
		latency.FirstResponseLatencyMs,
		latency.TotalResponseLatencyMs,
	)
	if err != nil {
		return err
	}
	return nil
}

// Records reads back all records in the usage database, in write order.
func (s *UsageStore) Records() ([]UsageRecord, error) {
	rows, err := s.conn.Query(`
		SELECT timestamp, response_id, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens, session_id, session_name,
			request_received_at, first_response_at, last_response_at, error_at, first_response_latency_ms, total_response_latency_ms
		FROM token_usage
		ORDER BY timestamp
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recs []UsageRecord
	for rows.Next() {
		var r tokenUsageRow
		if err := rows.Scan(
			&r.Timestamp,
			&r.ResponseID,
			&r.Status,
			&r.PromptTokens,
			&r.CompletionTokens,
			&r.TotalTokens,
			&r.CachedTokens,
			&r.SessionID,
			&r.SessionName,
			&r.RequestReceivedAt,
			&r.FirstResponseAt,
			&r.LastResponseAt,
			&r.ErrorAt,
			&r.FirstResponseLatencyMs,
			&r.TotalResponseLatencyMs,
		); err != nil {
			return nil, err
		}
		recs = append(recs, UsageRecord{
			Timestamp:   r.Timestamp,
			ResponseID:  r.ResponseID,
			Status:      r.Status,
			SessionID:   r.SessionID,
			SessionName: r.SessionName,
			Tokens: TokenUsage{
				PromptTokens:     r.PromptTokens,
				CompletionTokens: r.CompletionTokens,
				TotalTokens:      r.TotalTokens,
				CachedTokens:     r.CachedTokens,
			},
			Timing: ResponseTiming{
				RequestReceivedAt: r.RequestReceivedAt,
				FirstResponseAt:   r.FirstResponseAt,
				LastResponseAt:    r.LastResponseAt,
				ErrorAt:           r.ErrorAt,
			},
			Latency: ResponseLatency{
				FirstResponseLatencyMs: r.FirstResponseLatencyMs,
				TotalResponseLatencyMs: r.TotalResponseLatencyMs,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return recs, nil
}

// Close releases the usage database connection.
func (s *UsageStore) Close() error {
	return s.conn.Close()
}

// DBPath returns the path of the usage database file.
func (s *UsageStore) DBPath() string {
	return s.dbPath
}

// Conn returns a dedicated connection from the usage database connection
// pool. Connection-local operations such as attaching databases must run on a
// single connection, so callers should close the connection when done.
func (s *UsageStore) Conn(ctx context.Context) (*sql.Conn, error) {
	return s.conn.Conn(ctx)
}

// Query runs an arbitrary SQL query against the usage database and returns
// the result rows as a list of JSON object records (one map per row, keyed by
// column name).
func (s *UsageStore) Query(query string) ([]map[string]any, error) {
	rows, err := s.conn.QueryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	recs := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		rec := make(map[string]any, len(cols))
		for i, c := range cols {
			rec[c] = vals[i]
		}
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return recs, nil
}
