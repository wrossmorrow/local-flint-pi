package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/marcboeker/go-duckdb"
)

// UsageRecord is a timestamped token-usage observation as stored in the
// tabular usage database.
type UsageRecord struct {
	Timestamp  time.Time       `json:"timestamp"`
	ResponseID string          `json:"response_id,omitempty"`
	Status     string          `json:"status,omitempty"`
	Tokens     TokenUsage      `json:"tokens"`
	Timing     ResponseTiming  `json:"timing"`
	Latency    ResponseLatency `json:"latency"`
}

// TokenUsage is the token accounting nested under the "tokens" key of a usage
// record.
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt"`
	CompletionTokens int64 `json:"completion"`
	TotalTokens      int64 `json:"total"`
	CachedTokens     int64 `json:"cached"`
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
	conn *sql.DB
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
	return &UsageStore{conn: db}, nil
}

// Record appends a token usage record to the usage database.
func (s *UsageStore) Record(u *Usage, responseID string, status string, timing ResponseTiming, latency ResponseLatency) error {
	if u == nil {
		return errors.New("cannot record usage from nil usage")
	}
	ctx := context.Background()
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO token_usage (
			timestamp, response_id, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			request_received_at, first_response_at, last_response_at, error_at, first_response_latency_ms, total_response_latency_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		time.Now().UTC(),
		responseID,
		status,
		u.PromptTokens,
		u.CompletionTokens,
		u.TotalTokens,
		u.PromptTokensDetails.CachedTokens,
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
		SELECT timestamp, response_id, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
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
			Timestamp:  r.Timestamp,
			ResponseID: r.ResponseID,
			Status:     r.Status,
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
