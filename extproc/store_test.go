package main

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestUsageStoreRecordAndClose(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "usage.jsonl")
	store, err := NewUsageStore(path)
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	defer store.Close()

	if err := store.Record(&Usage{PromptTokens: 3, CompletionTokens: 8, TotalTokens: 11}, "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11"); err != nil {
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
	if recs[0].PromptTokens != 3 || recs[0].CompletionTokens != 8 || recs[0].TotalTokens != 11 {
		t.Errorf("Records()[0] = %+v, want prompt=3 completion=8 total=11", recs[0])
	}
	if recs[0].Timestamp.IsZero() {
		t.Errorf("Records()[0].Timestamp = %v, want non-zero", recs[0].Timestamp)
	}
}

func TestUsageStoreRecordNilUsage(t *testing.T) {
	t.Parallel()

	store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.jsonl"))
	if err != nil {
		t.Fatalf("NewUsageStore() error = %v", err)
	}
	defer store.Close()

	if err := store.Record(nil, "chatcmpl-1"); err == nil {
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
			if err := store.Record(&Usage{PromptTokens: int64(i), CompletionTokens: int64(i), TotalTokens: int64(i)}, ""); err != nil {
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
