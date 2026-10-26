package main

import "time"

// ResponseTiming holds the response timing observations for a single proxied
// response, as captured by the extproc. It is serialized into the collected
// output data (the token usage record) as the "timing" sub-structure.
type ResponseTiming struct {
	// RequestReceivedAt is when the request headers phase began processing.
	RequestReceivedAt time.Time `json:"request_received_at"`
	// FirstResponseAt is when the first response body chunk was received.
	FirstResponseAt time.Time `json:"first_response_at,omitempty"`
	// LastResponseAt is when the entire response body had been received.
	LastResponseAt time.Time `json:"last_response_at,omitempty"`
	// ErrorAt is when the stream errored, set when the response did not
	// finalize normally.
	ErrorAt time.Time `json:"error_at,omitempty"`
}

// ResponseLatency summarizes ResponseTiming as first and total response
// latencies, expressed in milliseconds. It is serialized into the collected
// output data (the token usage record) as the "latency" sub-structure.
type ResponseLatency struct {
	FirstResponseLatencyMs int64 `json:"first_response_latency_ms"`
	TotalResponseLatencyMs int64 `json:"total_response_latency_ms"`
}

// Latency summarizes the timing into first and total response latencies, in
// milliseconds. Both are zero when the corresponding timestamps are missing.
func (t ResponseTiming) Latency() ResponseLatency {
	return ResponseLatency{
		FirstResponseLatencyMs: t.FirstResponseLatencyMs(),
		TotalResponseLatencyMs: t.TotalResponseLatencyMs(),
	}
}

// FirstResponseLatency returns the time from the request being received to
// the first response body chunk being received.
func (t ResponseTiming) FirstResponseLatency() time.Duration {
	if t.RequestReceivedAt.IsZero() || t.FirstResponseAt.IsZero() {
		return 0
	}
	return t.FirstResponseAt.Sub(t.RequestReceivedAt)
}

// TotalResponseLatency returns the time from the request being received to
// the entire response body having been received.
func (t ResponseTiming) TotalResponseLatency() time.Duration {
	if t.RequestReceivedAt.IsZero() || t.LastResponseAt.IsZero() {
		return 0
	}
	return t.LastResponseAt.Sub(t.RequestReceivedAt)
}

// FirstResponseLatencyMs returns the first response latency in milliseconds.
func (t ResponseTiming) FirstResponseLatencyMs() int64 {
	return t.FirstResponseLatency().Milliseconds()
}

// TotalResponseLatencyMs returns the total response latency in milliseconds.
func (t ResponseTiming) TotalResponseLatencyMs() int64 {
	return t.TotalResponseLatency().Milliseconds()
}
