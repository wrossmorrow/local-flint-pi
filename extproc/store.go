package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// UsageRecord is a timestamped token-usage observation written to the WAL.
type UsageRecord struct {
	Timestamp        time.Time       `json:"timestamp"`
	ResponseID       string          `json:"response_id,omitempty"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	TotalTokens      int64           `json:"total_tokens"`
	CachedTokens     int64           `json:"cached_tokens"`
	Timing           ResponseTiming  `json:"timing"`
	Latency          ResponseLatency `json:"latency"`
}

// UsageStore appends token usage records to a write-ahead log (JSONL) file.
// It is safe for concurrent use and holds the log file open for appends.
type UsageStore struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewUsageStore opens (creating if necessary) the write-ahead log at path.
func NewUsageStore(path string) (*UsageStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &UsageStore{mu: sync.Mutex{}, path: path, file: f}, nil
}

// Record appends a token usage record to the WAL.
func (s *UsageStore) Record(u *Usage, responseID string, timing ResponseTiming, latency ResponseLatency) error {
	if u == nil {
		return errors.New("cannot record usage from nil usage")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := UsageRecord{
		Timestamp:        time.Now().UTC(),
		ResponseID:       responseID,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.PromptTokensDetails.CachedTokens,
		Timing:           timing,
		Latency:          latency,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.file.Sync()
}

// Records reads back all records in the WAL, in write order.
func (s *UsageStore) Records() ([]UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var recs []UsageRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r UsageRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// Close releases the write-ahead log file.
func (s *UsageStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
