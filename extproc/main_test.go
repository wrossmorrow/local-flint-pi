package main

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	ep "github.com/wrossmorrow/envoy-extproc-sdk-go"
)

type fakeValueStore struct {
	data map[string]any
}

func newFakeValueStore() *fakeValueStore {
	return &fakeValueStore{data: map[string]any{}}
}

func (f *fakeValueStore) GetValue(name string) (any, error) {
	v, ok := f.data[name]
	if !ok {
		return nil, errors.New(name + " does not exist")
	}
	return v, nil
}

func (f *fakeValueStore) SetValue(name string, val any) error {
	f.data[name] = val
	return nil
}

func makeAllHeaders(vals map[string]string) ep.AllHeaders {
	h := ep.AllHeaders{
		Headers:    make(map[string][]string, len(vals)),
		RawHeaders: make(map[string][][]byte, len(vals)),
	}
	for k, v := range vals {
		h.Headers[k] = []string{v}
		h.RawHeaders[k] = [][]byte{[]byte(v)}
	}
	return h
}

func TestProcessorRecordsModelAndProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		headers      map[string]string
		sseChunks    []string
		wantProvider string
		wantModel    string
		wantSession  string
	}{
		{
			name:    "defaults to rec and RecursiveSI/flint when headers and response model are absent",
			headers: map[string]string{"x-session-id": "sess-1", "x-session-name": "default-session"},
			sseChunks: []string{
				"data: {\"id\":\"chatcmpl-def\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n",
				"data: {\"id\":\"chatcmpl-def\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n",
			},
			wantProvider: "rec",
			wantModel:    "RecursiveSI/flint",
			wantSession:  "sess-1",
		},
		{
			name:    "normalizes short flint response model to RecursiveSI/flint",
			headers: map[string]string{"x-session-id": "sess-flint"},
			sseChunks: []string{
				"data: {\"id\":\"chatcmpl-flint\",\"model\":\"flint\",\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\n",
			},
			wantProvider: "rec",
			wantModel:    "RecursiveSI/flint",
			wantSession:  "sess-flint",
		},
		{
			name: "uses x-provider and x-model headers when present",
			headers: map[string]string{
				"x-session-id":   "sess-2",
				"x-session-name": "inkling-session",
				"x-provider":     "llama-server",
				"x-model":        "thinkingmachines/Inkling-2-Mini",
			},
			sseChunks: []string{
				"data: {\"id\":\"chatcmpl-ink\",\"model\":\"thinkingmachines/Inkling-2-Mini\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
				"data: {\"id\":\"chatcmpl-ink\",\"model\":\"thinkingmachines/Inkling-2-Mini\",\"usage\":{\"prompt_tokens\":25,\"completion_tokens\":12,\"total_tokens\":37}}\n\n",
				"data: [DONE]\n\n",
			},
			wantProvider: "llama-server",
			wantModel:    "thinkingmachines/Inkling-2-Mini",
			wantSession:  "sess-2",
		},
		{
			name:    "falls back to response json model when x-model header is omitted",
			headers: map[string]string{"x-session-id": "sess-3"},
			sseChunks: []string{
				"data: {\"id\":\"chatcmpl-resp\",\"model\":\"thinkingmachines/Inkling-2-Mini\",\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n",
			},
			wantProvider: "rec",
			wantModel:    "thinkingmachines/Inkling-2-Mini",
			wantSession:  "sess-3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewUsageStore(filepath.Join(t.TempDir(), "usage.duckdb"))
			if err != nil {
				t.Fatalf("NewUsageStore() error = %v", err)
			}
			defer store.Close()

			proc := &tokenCounterRequestProcessor{
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				store:  store,
			}
			ctx := newFakeValueStore()
			started := time.Now().Add(-50 * time.Millisecond)

			if err := proc.handleRequestHeaders(ctx, "POST", "/v1/chat/completions", started, makeAllHeaders(tt.headers)); err != nil {
				t.Fatalf("handleRequestHeaders() error = %v", err)
			}
			if err := proc.handleResponseHeaders(ctx); err != nil {
				t.Fatalf("handleResponseHeaders() error = %v", err)
			}
			for i, chunk := range tt.sseChunks {
				eos := i == len(tt.sseChunks)-1
				if err := proc.handleResponseBody(ctx, []byte(chunk), eos); err != nil {
					t.Fatalf("handleResponseBody(chunk %d) error = %v", i, err)
				}
			}

			recs, err := store.Records()
			if err != nil {
				t.Fatalf("Records() error = %v", err)
			}
			if len(recs) != 1 {
				t.Fatalf("Records() len = %d, want 1", len(recs))
			}
			if recs[0].Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", recs[0].Provider, tt.wantProvider)
			}
			if recs[0].Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", recs[0].Model, tt.wantModel)
			}
			if recs[0].SessionID != tt.wantSession {
				t.Errorf("SessionID = %q, want %q", recs[0].SessionID, tt.wantSession)
			}
		})
	}
}
