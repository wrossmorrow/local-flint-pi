package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory sessionStore for tests.
type fakeStore struct {
	mu       sync.Mutex
	puts     map[string][]byte
	contents map[string]string
	failKey  func(string) error
	putCount int
}

func newFakeStore() *fakeStore {
	return &fakeStore{puts: map[string][]byte{}, contents: map[string]string{}}
}

func (f *fakeStore) Put(_ context.Context, key string, data []byte, contentType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCount++
	if f.failKey != nil {
		if err := f.failKey(key); err != nil {
			return err
		}
	}
	f.puts[key] = data
	f.contents[key] = contentType
	return nil
}

func (f *fakeStore) get(key string) ([]byte, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.puts[key]
	ct, ok2 := f.contents[key]
	return b, ct, ok && ok2
}

func (f *fakeStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.puts))
	for k := range f.puts {
		out = append(out, k)
	}
	return out
}

func writeSessionFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const testSessionID = "019dbbc7-1111-2222-3333-444455556666"

func TestSessionIDFromFile(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/home/node/.pi/agent/sessions/--home--/2026-05-06T12-00-00-000Z_019dbbc7-1111-2222-3333-444455556666.jsonl", testSessionID},
		{"/tmp/sessions/ephemeral-1760000000000-abc123.jsonl", "ephemeral-1760000000000-abc123"},
	}
	for _, tc := range cases {
		if got := sessionIDFromFile(tc.path); got != tc.want {
			t.Fatalf("sessionIDFromFile(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestPartKey(t *testing.T) {
	if got := partKey(testSessionID, 0); got != "sessions/"+testSessionID+"/part-000000.jsonl.gz" {
		t.Fatalf("partKey(0) = %q", got)
	}
	if got := partKey(testSessionID, 41); got != "sessions/"+testSessionID+"/part-000041.jsonl.gz" {
		t.Fatalf("partKey(41) = %q", got)
	}
}

func TestSessionUploaderFlushShipsPart(t *testing.T) {
	content := []byte("{\"type\":\"message_start\",\"message\":{\"role\":\"user\"}}\n{\"type\":\"message_end\",\"message\":{\"role\":\"user\"}}\n")
	path := writeSessionFile(t, testSessionID+".jsonl", string(content))

	u := newSessionUploader(newFakeStore(), time.Hour, defaultSessionMaxPartBytes, func(context.Context) (string, error) { return path, nil })
	st, err := u.Flush(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if st.sessionKey != testSessionID {
		t.Fatalf("sessionKey = %q, want %q", st.sessionKey, testSessionID)
	}
	if st.lastOffset != int64(len(content)) {
		t.Fatalf("lastOffset = %d, want %d", st.lastOffset, len(content))
	}
	if st.partCount != 1 {
		t.Fatalf("partCount = %d, want 1", st.partCount)
	}
	if !st.shipped {
		t.Fatal("expected shipped = true")
	}

	fake := u.store.(*fakeStore)
	b, ct, ok := fake.get(partKey(testSessionID, 0))
	if !ok {
		t.Fatalf("part %q not uploaded", partKey(testSessionID, 0))
	}
	if ct != partContentType {
		t.Fatalf("part content type = %q, want %q", ct, partContentType)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("part is not gzip: %v", err)
	}
	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("part payload = %q, want %q", got, content)
	}

	sum := sha256.Sum256(content)
	if st.hashHex() != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %q, want %q", st.hashHex(), hex.EncodeToString(sum[:]))
	}

	// a flush with no new content ships nothing further
	st2, err := u.Flush(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if st2.shipped || st2.partCount != 1 || st2.lastOffset != int64(len(content)) {
		t.Fatalf("no-op flush changed state: %+v", st2)
	}
}

func TestSessionUploaderSplitsLargeParts(t *testing.T) {
	content := []byte("{\"a\":1}\n{\"b\":22}\n{\"c\":333}\n")
	path := writeSessionFile(t, testSessionID+".jsonl", string(content))
	u := newSessionUploader(newFakeStore(), time.Hour, 20, func(context.Context) (string, error) { return path, nil })
	st, err := u.Flush(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// limit 20 bytes: the first part holds the two lines that fit, the third
	// ships as its own part at the offset where it starts.
	if st.partCount != 2 {
		t.Fatalf("partCount = %d, want 2 (split at the size limit)", st.partCount)
	}
	if st.lastOffset != int64(len(content)) {
		t.Fatalf("lastOffset = %d, want %d", st.lastOffset, len(content))
	}
	fake := u.store.(*fakeStore)
	b0, ct0, ok0 := fake.get(partKey(testSessionID, 0))
	if !ok0 {
		t.Fatalf("expected part at offset 0, got keys %v", fake.keys())
	}
	if ct0 != partContentType {
		t.Fatalf("part content type = %q, want %q", ct0, partContentType)
	}
	gz0, err := gzip.NewReader(bytes.NewReader(b0))
	if err != nil {
		t.Fatalf("part is not gzip: %v", err)
	}
	got0, err := io.ReadAll(gz0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got0, content[:17]) {
		t.Fatalf("part0 payload = %q, want %q", got0, content[:17])
	}
	b1, _, ok1 := fake.get(partKey(testSessionID, 17))
	if !ok1 {
		t.Fatalf("expected part at offset 17, got keys %v", fake.keys())
	}
	gz1, err := gzip.NewReader(bytes.NewReader(b1))
	if err != nil {
		t.Fatalf("part is not gzip: %v", err)
	}
	got1, err := io.ReadAll(gz1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, content[17:]) {
		t.Fatalf("part1 payload = %q, want %q", got1, content[17:])
	}
}

func TestSessionUploaderCloseWritesClosedMarker(t *testing.T) {
	content := []byte("{\"type\":\"message_end\"}\n")
	path := writeSessionFile(t, testSessionID+".jsonl", string(content))
	u := newSessionUploader(newFakeStore(), time.Hour, defaultSessionMaxPartBytes, func(context.Context) (string, error) { return path, nil })
	if _, err := u.Flush(context.Background(), path); err != nil {
		t.Fatal(err)
	}

	if err := u.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake := u.store.(*fakeStore)
	b, ct, ok := fake.get(closedKey(testSessionID))
	if !ok {
		t.Fatalf("_closed not written, keys = %v", fake.keys())
	}
	if ct != closedContentType {
		t.Fatalf("_closed content type = %q, want %q", ct, closedContentType)
	}
	var cr closedReport
	if err := json.Unmarshal(b, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Status != "ok" {
		t.Fatalf("_closed status = %q, want ok", cr.Status)
	}
	if cr.Error != "" {
		t.Fatalf("_closed error = %q, want empty", cr.Error)
	}
	if cr.FinalOffset != int64(len(content)) {
		t.Fatalf("_closed finalOffset = %d, want %d", cr.FinalOffset, len(content))
	}
	if cr.PartCount != 1 {
		t.Fatalf("_closed partCount = %d, want 1", cr.PartCount)
	}
	sum := sha256.Sum256(content)
	if cr.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("_closed hash = %q, want %q", cr.Hash, hex.EncodeToString(sum[:]))
	}
}

func TestSessionUploaderCloseMarksError(t *testing.T) {
	path := writeSessionFile(t, testSessionID+".jsonl", "{\"type\":\"message_end\"}\n")
	fake := newFakeStore()
	fake.failKey = func(key string) error {
		if strings.Contains(key, "/part-") {
			return errors.New("boom")
		}
		return nil
	}
	u := newSessionUploader(fake, time.Hour, defaultSessionMaxPartBytes, func(context.Context) (string, error) { return path, nil })
	if _, err := u.Flush(context.Background(), path); err == nil {
		t.Fatal("expected flush error")
	}
	if err := u.Close(context.Background()); err == nil {
		t.Fatal("expected close error")
	}
	b, _, ok := fake.get(closedKey(testSessionID))
	if !ok {
		t.Fatal("_closed not written after failed flush")
	}
	var cr closedReport
	if err := json.Unmarshal(b, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Status != "error" {
		t.Fatalf("_closed status = %q, want error", cr.Status)
	}
	if cr.Error == "" {
		t.Fatal("_closed error should carry the failure message")
	}
	if cr.FinalOffset != 0 || cr.PartCount != 0 {
		t.Fatalf("_closed after failed flush = %+v", cr)
	}
}

func TestSessionUploaderFlushDetectsShrink(t *testing.T) {
	path := writeSessionFile(t, testSessionID+".jsonl", "{\"type\":\"message_end\"}\n")
	u := newSessionUploader(newFakeStore(), time.Hour, defaultSessionMaxPartBytes, func(context.Context) (string, error) { return path, nil })
	if _, err := u.Flush(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("shorter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Flush(context.Background(), path); err == nil {
		t.Fatal("expected error when the session file shrinks")
	}
}
