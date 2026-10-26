package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryHandler(t *testing.T) {
	t.Parallel()

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
	if err := store.Record(&Usage{PromptTokens: 3, CompletionTokens: 8, TotalTokens: 11}, "chatcmpl-query-1", "ok", timing, latency); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	srv := httptest.NewServer(queryHandler(store))
	defer srv.Close()

	body, err := json.Marshal(map[string]string{"query": "SELECT * FROM token_usage"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/query", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.DefaultClient.Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var got struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	if len(got.Records) != 1 {
		t.Fatalf("records len = %d, want 1", len(got.Records))
	}
	if got.Records[0]["response_id"] != "chatcmpl-query-1" {
		t.Errorf("records[0][response_id] = %#v, want chatcmpl-query-1", got.Records[0]["response_id"])
	}
}
