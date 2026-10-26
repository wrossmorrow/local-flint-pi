package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUsageStoreRecordAndClose(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "usage.jsonl")
	store, err := NewUsageStore(path)
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
	if err := store.Record(&Usage{PromptTokens: 3, CompletionTokens: 8, TotalTokens: 11}, "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11", "ok", timing, latency); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	recs, err := store.Records()
	if err != nil {
		t.Fatalf("Records() error = %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("Records() len = %d, want 1", len(recs))
	}
	if recs[0].ResponseID != "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11" {
		t.Errorf("Records()[0].ResponseID = %q, want chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11", recs[0].ResponseID)
	}
	if recs[0].Tokens.PromptTokens != 3 || recs[0].Tokens.CompletionTokens != 8 || recs[0].Tokens.TotalTokens != 11 {
		t.Errorf("Records()[0].Tokens = %+v, want prompt=3 completion=8 total=11", recs[0].Tokens)
	}
	if recs[0].Timestamp.IsZero() {
		t.Errorf("Records()[0].Timestamp = %v, want non-zero", recs[0].Timestamp)
	}
	if !recs[0].Timing.RequestReceivedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Records()[0].Timing.RequestReceivedAt = %v, want 2026-01-01T00:00:00Z", recs[0].Timing.RequestReceivedAt)
	}
	if !recs[0].Timing.FirstResponseAt.Equal(time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)) {
		t.Errorf("Records()[0].Timing.FirstResponseAt = %v, want 2026-01-01T00:00:01Z", recs[0].Timing.FirstResponseAt)
	}
	if !recs[0].Timing.LastResponseAt.Equal(time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)) {
		t.Errorf("Records()[0].Timing.LastResponseAt = %v, want 2026-01-01T00:00:03Z", recs[0].Timing.LastResponseAt)
	}
	if recs[0].Status != "ok" {
		t.Errorf("Records()[0].Status = %q, want ok", recs[0].Status)
	}
	if recs[0].Tokens.PromptTokens != 3 || recs[0].Tokens.CompletionTokens != 8 || recs[0].Tokens.TotalTokens != 11 || recs[0].Tokens.CachedTokens != 0 {
		t.Errorf("Records()[0].Tokens = %+v, want prompt=3 completion=8 total=11 cached=0", recs[0].Tokens)
	}
	if recs[0].Latency.FirstResponseLatencyMs != 1000 {
		t.Errorf("Records()[0].Latency.FirstResponseLatencyMs = %d, want 1000", recs[0].Latency.FirstResponseLatencyMs)
	}
	if recs[0].Latency.TotalResponseLatencyMs != 3000 {
		t.Errorf("Records()[0].Latency.TotalResponseLatencyMs = %d, want 3000", recs[0].Latency.TotalResponseLatencyMs)
	}
}

func TestUsageStoreRecordNilUsage(t *testing.T) {
	t.Parallel()

	store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.jsonl"))
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	defer store.Close()

	if err := store.Record(nil, "chatcmpl-1", "ok", ResponseTiming{}, ResponseLatency{}); err == nil {
		t.Fatal("Record() expected error for nil usage, got nil")
	}
}

func TestUsageStoreConcurrentRecords(t *testing.T) {
	t.Parallel()

	store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.jsonl"))
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	defer store.Close()

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Record(&Usage{PromptTokens: int64(i), CompletionTokens: int64(i), TotalTokens: int64(i)}, "", "ok", ResponseTiming{}, ResponseLatency{}); err != nil {
				t.Errorf("Record() error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	recs, err := store.Records()
	if err != nil {
		t.Fatalf("Records() error = %v", err)
	}
	if len(recs) != n {
		t.Fatalf("Records() len = %d, want %d", len(recs), n)
	}
}
