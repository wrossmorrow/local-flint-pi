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
	// byte offset in the session file at which the part begins. Parts live in
	// a parts/ sub-folder of the session prefix; the padding is wide enough
	// that the names stay fixed-width and ordered for the lifetime of a
	// session.
	partKeyFormat = "sessions/%s/parts/part-%012d.jsonl.gz"
	// closedKeyFormat is the shutdown marker for a session.
	closedKeyFormat = "sessions/%s/_closed"
	// metadataKeyFormat is the session metadata object written at startup and
	// updated at close.
	metadataKeyFormat = "sessions/%s/metadata.json"
	// partContentType is the content type of uploaded part objects; they hold
	// gzipped JSONL.
	partContentType = "application/gzip"
	// closedContentType is the content type of the _closed marker object.
	closedContentType = "application/json"
)

// sessionRef is the live pi session the uploader ships: where its file lives,
// the RPC state/stats snapshots for it, and the static session attributes.
type sessionRef struct {
	Path             string
	State            map[string]any
	Stats            map[string]any
	Repo             string
	AgentEnvironment map[string]any
	AgentIdentity    string
	ClientHost       string
}

// sessionState is the per-session upload bookkeeping: where the uploader is
// in the (append-only) session file, and what has shipped so far. A session
// file must never shrink, so lastOffset only ever moves forward. It also
// carries the session metadata written at startup and updated at close.
type sessionState struct {
	sessionKey string
	path       string
	lastOffset int64 // byte offset just past the last complete line shipped
	partCount  int64
	hash       hash.Hash // running sha256 over the shipped JSONL bytes
	shipped    bool      // whether the latest flush shipped anything
	closed     bool      // whether the session has been closed via the close trigger / sweep
	metadata   *sessionMetadata
}

// sessionMetadata is the sessions/<id>/metadata.json object: the values
// captured at session startup and updated at close. Start fields are set at
// session start; end fields (ended_at, end_reason, final totals) are merged
// in when the session closes. Updates are non-destructive: the start fields
// are only written when the session is first seen, later writes update the
// same object.
type sessionMetadata struct {
	SessionID        string         `json:"session_id"`
	SessionName      string         `json:"session_name"`
	Repo             string         `json:"repo"`
	AgentEnvironment map[string]any `json:"agent_environment,omitempty"`
	ModelConfig      map[string]any `json:"model_config,omitempty"`
	AgentIdentity    string         `json:"agent_identity,omitempty"`
	ClientHost       string         `json:"client_host,omitempty"`
	StartedAt        string         `json:"started_at,omitempty"`
	EndedAt          string         `json:"ended_at,omitempty"`
	EndReason        string         `json:"end_reason,omitempty"`
	Tokens           map[string]any `json:"tokens,omitempty"`
	ToolCalls        int64          `json:"tool_calls,omitempty"`
	MessageCounts    map[string]any `json:"message_counts,omitempty"`
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

	// source resolves the current session (path, state, stats, attributes).
	source func(context.Context) (*sessionRef, error)

	mu       sync.Mutex
	sessions map[string]*sessionState

	cancel   context.CancelFunc
	loopDone chan struct{}
}

func newSessionUploader(store sessionStore, flushInterval time.Duration, maxPartBytes int64, source func(context.Context) (*sessionRef, error)) *sessionUploader {
	if flushInterval <= 0 {
		flushInterval = defaultSessionFlushInterval
	}
	return &sessionUploader{
		store:         store,
		flushInterval: flushInterval,
		maxPartBytes:  maxPartBytes,
		source:        source,
		sessions:      map[string]*sessionState{},
	}
}

func (u *sessionUploader) Enabled() bool { return u.store != nil }

// IsClosed reports whether the session has been closed via the close trigger
// or the shutdown sweep and therefore accepts no further user messages.
func (u *sessionUploader) IsClosed(sessionKey string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, ok := u.sessions[sessionKey]
	return ok && st.closed
}

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
// failed). It also updates and writes the session metadata with the end
// reason and final totals.
func (u *sessionUploader) Close(ctx context.Context, endReason string) error {
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
	endedAt := time.Now().UTC().Format(time.RFC3339)
	ref, rerr := u.source(ctx)
	refKey, refState, refStats := "", map[string]any{}, map[string]any{}
	if rerr == nil && ref != nil {
		refKey = sessionIDFromFile(ref.Path)
		refState = ref.State
		refStats = ref.Stats
	}
	for _, k := range keys {
		log.Printf("session upload close: closing session %s (endReason %q)", k, endReason)
		st := u.sessions[k]
		st.closed = true
		final, err := u.flushLocked(ctx, st)
		meta := st.metadata
		if meta == nil {
			meta = &sessionMetadata{SessionID: k}
			st.metadata = meta
		}
		name := meta.SessionName
		if k == refKey {
			name = sessionNameFromState(refState)
		}
		meta.SessionName = name
		meta.EndedAt = endedAt
		meta.EndReason = endReason
		meta.Tokens = tokensFromStats(refStats)
		meta.ToolCalls = toolCallsFromStats(refStats)
		meta.MessageCounts = messageCountsFromStats(refStats)
		b, mErr := json.Marshal(meta)
		if mErr != nil {
			if firstErr == nil {
				firstErr = mErr
			}
			continue
		}
		if err := u.store.Put(ctx, metadataKey(k), b, "application/json"); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("close session %s: %w", k, err)
			}
		}
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
		cb, cErr := json.Marshal(rep)
		if cErr != nil {
			if firstErr == nil {
				firstErr = cErr
			}
			continue
		}
		if err := u.store.Put(ctx, closedKey(k), cb, closedContentType); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("close session %s: %w", k, err)
			}
		} else {
			log.Printf("session upload close: wrote %s status %q", closedKey(k), rep.Status)
		}
	}
	return firstErr
}

// CloseSession performs an explicit close of one tracked session: it ships
// any content pending for the session and writes its sessions/<id>/_closed
// marker (status ok, or error with the failure message if the final flush
// failed). It does not stop the periodic flush loop. It is the close trigger
// behind the close endpoint and is a no-op for sessions that are not tracked.
func (u *sessionUploader) CloseSession(ctx context.Context, sessionKey string, endReason string, ref *sessionRef) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, ok := u.sessions[sessionKey]
	if !ok {
		return nil
	}
	log.Printf("session upload close: closing session %s (endReason %q)", sessionKey, endReason)
	final, err := u.flushLocked(ctx, st)
	meta := st.metadata
	if meta == nil {
		meta = &sessionMetadata{SessionID: sessionKey}
		st.metadata = meta
	}
	name := meta.SessionName
	if ref != nil && sessionKey == sessionIDFromFile(ref.Path) {
		name = sessionNameFromState(ref.State)
	}
	meta.SessionName = name
	meta.EndedAt = time.Now().UTC().Format(time.RFC3339)
	meta.EndReason = endReason
	if ref != nil {
		meta.Tokens = tokensFromStats(ref.Stats)
		meta.ToolCalls = toolCallsFromStats(ref.Stats)
		meta.MessageCounts = messageCountsFromStats(ref.Stats)
	}
	b, mErr := json.Marshal(meta)
	if mErr != nil {
		return mErr
	}
	if err := u.store.Put(ctx, metadataKey(sessionKey), b, "application/json"); err != nil {
		return fmt.Errorf("close session %s: %w", sessionKey, err)
	}
	rep := closedReport{
		Status:      "ok",
		FinalOffset: final.lastOffset,
		PartCount:   final.partCount,
		Hash:        final.hashHex(),
	}
	if err != nil {
		rep.Status = "error"
		rep.Error = err.Error()
	}
	cb, cErr := json.Marshal(rep)
	if cErr != nil {
		return cErr
	}
	if err := u.store.Put(ctx, closedKey(sessionKey), cb, closedContentType); err != nil {
		return fmt.Errorf("close session %s: %w", sessionKey, err)
	}
	st.closed = true
	log.Printf("session upload close: wrote %s status %q", closedKey(sessionKey), rep.Status)
	return nil
}

// Flush ships everything in the session at ref since the tracked offset and
// returns the resulting state. Content is shipped as parts of at most
// maxPartBytes (split at line boundaries, never mid-record); a final partial
// part stays pending until the next flush. An error is returned if the file
// could not be read or a part failed to upload. When a session is first seen
// its startup metadata is written.
func (u *sessionUploader) Flush(ctx context.Context, ref *sessionRef) (*sessionState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.store == nil {
		return &sessionState{sessionKey: sessionIDFromFile(ref.Path)}, nil
	}
	key := sessionIDFromFile(ref.Path)
	st := u.sessions[key]
	if st == nil {
		st = &sessionState{sessionKey: key, hash: sha256.New()}
		u.sessions[key] = st
		st.path = ref.Path
		// At session start, check for an existing metadata object and adopt it
		// (e.g. when the server comes back up mid-session); updates are otherwise
		// non-destructive, since close only overwrites the end-time fields.
		meta := &sessionMetadata{SessionID: key}
		if existing, err := u.store.Get(ctx, metadataKey(key)); err == nil {
			if err := json.Unmarshal(existing, meta); err == nil {
				st.metadata = meta
			}
		}
		if st.metadata == nil {
			st.metadata = meta
		}
		// A session that was closed before this process started (via the close
		// trigger or the shutdown sweep, and shipped to object storage) is still
		// closed: adopting the _closed marker keeps a restarted server from
		// treating a closed session as open again.
		if existing, err := u.store.Get(ctx, closedKey(key)); err == nil {
			var cr closedReport
			if err := json.Unmarshal(existing, &cr); err == nil {
				st.closed = true
			}
		}
		m := st.metadata
		m.SessionName = sessionNameFromState(ref.State)
		m.Repo = ref.Repo
		m.AgentEnvironment = ref.AgentEnvironment
		m.ModelConfig = modelConfigFromState(ref.State)
		m.AgentIdentity = ref.AgentIdentity
		m.ClientHost = ref.ClientHost
		m.StartedAt = time.Now().UTC().Format(time.RFC3339)
		m.EndedAt = ""
		m.EndReason = ""
		m.Tokens = nil
		m.ToolCalls = 0
		m.MessageCounts = nil
		b, mErr := json.Marshal(m)
		if mErr != nil {
			return st, mErr
		}
		if err := u.store.Put(ctx, metadataKey(key), b, "application/json"); err != nil {
			return st, fmt.Errorf("write session metadata %s: %w", metadataKey(key), err)
		}
	}
	st.path = ref.Path
	return u.flushLocked(ctx, st)
}

func (u *sessionUploader) tick(ctx context.Context) {
	if !u.Enabled() {
		return
	}
	ref, err := u.source(ctx)
	if err != nil || ref == nil || ref.Path == "" {
		return
	}
	if _, err := u.Flush(ctx, ref); err != nil {
		log.Printf("session upload flush failed for %s: %v", ref.Path, err)
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
			// the pending part would exceed the size limit, so ship it before
			// adding this line to it
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
// offset at which the part begins, as sessions/<id>/parts/part-<offset>.jsonl.gz.
func partKey(sessionKey string, offset int64) string {
	return fmt.Sprintf(partKeyFormat, sessionKey, offset)
}

// closedKey returns the S3 key of a session's close marker.
func closedKey(sessionKey string) string {
	return fmt.Sprintf(closedKeyFormat, sessionKey)
}

// metadataKey returns the S3 key of a session's metadata object.
func metadataKey(sessionKey string) string {
	return fmt.Sprintf(metadataKeyFormat, sessionKey)
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

// sessionNameFromState reads the human-readable session name from a get_state
// snapshot.
func sessionNameFromState(state map[string]any) string {
	if state == nil {
		return ""
	}
	if v, ok := state["sessionName"].(string); ok {
		return v
	}
	if v, ok := state["name"].(string); ok {
		return v
	}
	return ""
}

// modelConfigFromState reads the provider/model and context-window/token
// settings out of a get_state snapshot (context window / max tokens also come
// from REC_PI_CONFIG).
func modelConfigFromState(state map[string]any) map[string]any {
	mc := map[string]any{}
	if state != nil {
		if v, ok := state["model"].(map[string]any); ok {
			if provider, ok := v["provider"].(string); ok {
				mc["provider"] = provider
			}
			if id, ok := v["modelId"].(string); ok {
				mc["modelId"] = id
			}
			if id, ok := v["id"].(string); ok {
				mc["modelId"] = id
			}
		}
	}
	cfg := map[string]any{}
	if raw := os.Getenv("REC_PI_CONFIG"); raw != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, k := range []string{"contextWindow", "maxTokens", "baseUrl"} {
				if v, ok := parsed[k]; ok {
					cfg[k] = v
				}
			}
		}
	}
	for k, v := range cfg {
		mc[k] = v
	}
	return mc
}

// tokensFromStats extracts the token totals from a get_session_stats snapshot.
func tokensFromStats(stats map[string]any) map[string]any {
	tokens := map[string]any{}
	if stats == nil {
		return tokens
	}
	if v, ok := stats["tokens"].(map[string]any); ok {
		out := map[string]any{}
		for k, val := range v {
			out[k] = val
		}
		tokens = out
	}
	return tokens
}

// toolCallsFromStats extracts the tool-call total from a get_session_stats
// snapshot.
func toolCallsFromStats(stats map[string]any) int64 {
	var n int64
	if stats == nil {
		return n
	}
	switch v := stats["toolCalls"].(type) {
	case float64:
		n = int64(v)
	case int64:
		n = v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			n = i
		}
	}
	return n
}

// messageCountsFromStats extracts per-role message counts from a
// get_session_stats snapshot.
func messageCountsFromStats(stats map[string]any) map[string]any {
	counts := map[string]any{}
	if stats == nil {
		return counts
	}
	for _, k := range []string{"userMessages", "assistantMessages", "toolCalls", "toolResults", "totalMessages"} {
		switch v := stats[k].(type) {
		case float64:
			counts[k] = int64(v)
		case int64:
			counts[k] = v
		case json.Number:
			if i, err := v.Int64(); err == nil {
				counts[k] = i
			}
		}
	}
	return counts
}
