package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// defaultSessionFlushInterval is how often the uploader checks the
	// session file for content that has not been shipped yet.
	defaultSessionFlushInterval = 30 * time.Second
	// defaultSessionMaxPartBytes is the part size at which new content is
	// split into multiple parts. Parts at or under this size upload with a
	// single PutObject and need no S3 multipart upload.
	defaultSessionMaxPartBytes = 5 * 1024 * 1024
	// partKeyFormat names an uploaded part. The number is the zero-padded
	// byte offset in the session file at which the part begins.
	partKeyFormat = "sessions/%s/part-%06d.jsonl.gz"
	// closedKeyFormat is the shutdown marker for a session.
	closedKeyFormat = "sessions/%s/_closed"
	// partContentType is the content type of uploaded part objects; they hold
	// gzipped JSONL.
	partContentType = "application/gzip"
	// closedContentType is the content type of the _closed marker object.
	closedContentType = "application/json"
)

// sessionState is the per-session upload bookkeeping: where the uploader is
// in the (append-only) session file, and what has shipped so far. A session
// file must never shrink, so lastOffset only ever moves forward.
type sessionState struct {
	sessionKey string
	path       string
	lastOffset int64 // byte offset just past the last complete line shipped
	partCount  int64
	hash       hash.Hash // running sha256 over the shipped JSONL bytes
	shipped    bool      // whether the latest flush shipped anything
}

// closedReport is the sessions/<id>/_closed object written when a session is
// closed: the final offset, part count, content hash, and status.
type closedReport struct {
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	FinalOffset int64  `json:"finalOffset"`
	PartCount   int64  `json:"partCount"`
	Hash        string `json:"hash"`
}

// sessionUploader ships pi session JSONL parts to object storage. It tracks
// per-session offsets into the session file and flushes new content either
// periodically or once the pending batch grows past the part size limit. It is
// inert when no bucket is configured.
type sessionUploader struct {
	store sessionStore

	flushInterval time.Duration
	maxPartBytes  int64

	// source resolves the current session file path. Called once per flush
	// tick and from the flush endpoint.
	source func(context.Context) (string, error)

	mu       sync.Mutex
	sessions map[string]*sessionState

	cancel   context.CancelFunc
	loopDone chan struct{}
}

func newSessionUploader(store sessionStore, flushInterval time.Duration, maxPartBytes int64, source func(context.Context) (string, error)) *sessionUploader {
	return &sessionUploader{
		store:         store,
		flushInterval: flushInterval,
		maxPartBytes:  maxPartBytes,
		source:        source,
		sessions:      map[string]*sessionState{},
	}
}

func (u *sessionUploader) Enabled() bool { return u.store != nil }

// Start begins the periodic flush loop and returns a stop function that waits
// for the loop to wind down.
func (u *sessionUploader) Start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	u.cancel = cancel
	u.loopDone = make(chan struct{})
	go func() {
		defer close(u.loopDone)
		t := time.NewTicker(u.flushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				u.tick(ctx)
			}
		}
	}()
	return func() { cancel(); <-u.loopDone }
}

// Stop halts the periodic flush loop.
func (u *sessionUploader) Stop() {
	if u.cancel != nil {
		u.cancel()
	}
	if u.loopDone != nil {
		<-u.loopDone
	}
}

// Close halts the flush loop and performs the final close sweep: ship any
// content still pending for a session and write its sessions/<id>/_closed
// marker (status ok, or error with the failure message if the final flush
// failed).
func (u *sessionUploader) Close(ctx context.Context) error {
	u.Stop()
	if u.store == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	keys := make([]string, 0, len(u.sessions))
	for k, st := range u.sessions {
		if st.path != "" {
			keys = append(keys, k)
		}
	}
	var firstErr error
	for _, k := range keys {
		st := u.sessions[k]
		final, err := u.flushLocked(ctx, st)
		rep := closedReport{
			Status:      "ok",
			FinalOffset: final.lastOffset,
			PartCount:   final.partCount,
			Hash:        final.hashHex(),
		}
		if err != nil {
			rep.Status = "error"
			rep.Error = err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("close session %s: %w", k, err)
			}
		}
		b, mErr := json.Marshal(rep)
		if mErr != nil {
			if firstErr == nil {
				firstErr = mErr
			}
			continue
		}
		if err := u.store.Put(ctx, closedKey(k), b, closedContentType); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("close session %s: %w", k, err)
			}
		}
	}
	return firstErr
}

// Flush ships everything in the session file at path since the tracked offset
// and returns the resulting state. Content is shipped as parts of at most
// maxPartBytes (split at line boundaries, never mid-record); a final partial
// part stays pending until the next flush. An error is returned if the file
// could not be read or a part failed to upload.
func (u *sessionUploader) Flush(ctx context.Context, path string) (*sessionState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.store == nil {
		return &sessionState{sessionKey: sessionIDFromFile(path)}, nil
	}
	key := sessionIDFromFile(path)
	st := u.sessions[key]
	if st == nil {
		st = &sessionState{sessionKey: key, hash: sha256.New()}
		u.sessions[key] = st
	}
	st.path = path
	return u.flushLocked(ctx, st)
}

func (u *sessionUploader) tick(ctx context.Context) {
	if !u.Enabled() {
		return
	}
	path, err := u.source(ctx)
	if err != nil {
		return
	}
	if path == "" {
		return
	}
	if _, err := u.Flush(ctx, path); err != nil {
		log.Printf("session upload flush failed for %s: %v", path, err)
	}
}

// flushLocked ships the content of st's session file that has not been shipped
// yet, in parts of at most maxPartBytes. On success st.lastOffset points just
// past the last shipped byte, st.partCount counts the parts shipped, and st.hash
// is the running sha256 over the shipped JSONL bytes. An error is returned if the
// file shrank or a part failed to upload.
func (u *sessionUploader) flushLocked(ctx context.Context, st *sessionState) (*sessionState, error) {
	st.shipped = false
	data, err := os.ReadFile(st.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// the session file has not been created yet (session just started)
			return st, nil
		}
		return st, fmt.Errorf("read session file %s: %w", st.path, err)
	}
	if int64(len(data)) < st.lastOffset {
		// A session file must be append-only; if it ever shrinks below the
		// last shipped offset the tracked position is no longer meaningful.
		return st, fmt.Errorf("session file shrank from offset %d to %d", st.lastOffset, len(data))
	}
	end := endOffset(data, st.lastOffset)
	if end <= st.lastOffset {
		return st, nil // nothing new since the last shipped offset
	}
	content := data[st.lastOffset:end]
	rest := content
	partStart := st.lastOffset
	part := []byte(nil)
	shippedAny := false
	for len(rest) > 0 {
		var line []byte
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			line = rest[:idx+1]
			rest = rest[idx+1:]
		} else {
			line = rest
			rest = nil
		}
		if len(part) > 0 && int64(len(part))+int64(len(line)) > u.maxPartBytes {
			if err := u.ship(ctx, st, part, partStart); err != nil {
				return st, err
			}
			shippedAny = true
			part = nil
			partStart = st.lastOffset
		}
		if len(part) == 0 && int64(len(line)) > u.maxPartBytes {
			// A single record larger than the part limit is shipped whole rather
			// than splitting mid-record.
			if err := u.ship(ctx, st, line, partStart); err != nil {
				return st, err
			}
			shippedAny = true
			partStart = st.lastOffset
			continue
		}
		part = append(part, line...)
	}
	if len(part) > 0 {
		if err := u.ship(ctx, st, part, partStart); err != nil {
			return st, err
		}
		shippedAny = true
	}
	st.shipped = shippedAny
	return st, nil
}

// ship uploads one part and advances the session state past it.
func (u *sessionUploader) ship(ctx context.Context, st *sessionState, part []byte, partStart int64) error {
	key := partKey(st.sessionKey, partStart)
	gz, err := gzipBytes(part)
	if err != nil {
		return fmt.Errorf("gzip part %s: %w", key, err)
	}
	if err := u.store.Put(ctx, key, gz, partContentType); err != nil {
		return fmt.Errorf("upload part %s: %w", key, err)
	}
	st.hash.Write(part)
	st.partCount++
	st.lastOffset = partStart + int64(len(part))
	st.shipped = true
	return nil
}

// endOffset returns the byte offset just past the last complete line in data
// at or after `from`. A trailing partial line (no terminating newline yet) is
// treated as pending. from must be a line boundary (or 0).
func endOffset(data []byte, from int64) int64 {
	start := int(from)
	if start > len(data) {
		return from
	}
	idx := bytes.LastIndexByte(data[start:], '\n')
	if idx < 0 {
		return from
	}
	return int64(start + idx + 1)
}

// sessionIDFromFile derives the stable session id from a pi session file path
// such as ~/.pi/agent/sessions/--home--/2026-05-06T12-00-00-000Z_019dbbc7-....jsonl
// -> "019dbbc7-...". Falls back to the full file stem for ephemeral sessions.
func sessionIDFromFile(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if i := strings.Index(stem, "_"); i >= 0 {
		if id := strings.TrimPrefix(stem[i+1:], "_"); id != "" {
			return id
		}
	}
	return stem
}

// partKey returns the S3 key for one part of a session: the zero-padded byte
// offset at which the part begins, as sessions/<id>/part-<offset>.jsonl.gz.
func partKey(sessionKey string, offset int64) string {
	return fmt.Sprintf(partKeyFormat, sessionKey, offset)
}

// closedKey returns the S3 key of a session's close marker.
func closedKey(sessionKey string) string {
	return fmt.Sprintf(closedKeyFormat, sessionKey)
}

// gzipBytes gzips b.
func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (st *sessionState) hashHex() string {
	return hex.EncodeToString(st.hash.Sum(nil))
}
