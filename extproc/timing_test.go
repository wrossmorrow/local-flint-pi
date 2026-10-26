package main

import (
	"testing"
	"time"
)

func TestResponseTimingLatency(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("zero timing has no latencies", func(t *testing.T) {
		t.Parallel()

		timing := ResponseTiming{}
		if got := timing.FirstResponseLatency(); got != 0 {
			t.Errorf("ResponseTiming.FirstResponseLatency() = %v, want 0", got)
		}
		if got := timing.TotalResponseLatency(); got != 0 {
			t.Errorf("ResponseTiming.TotalResponseLatency() = %v, want 0", got)
		}
		if got := timing.Latency(); got != (ResponseLatency{}) {
			t.Errorf("ResponseTiming.Latency() = %+v, want %+v", got, ResponseLatency{})
		}
	})

	t.Run("first and total response latencies in milliseconds", func(t *testing.T) {
		t.Parallel()

		timing := ResponseTiming{
			RequestReceivedAt: base,
			FirstResponseAt:   base.Add(1500 * time.Millisecond),
			LastResponseAt:    base.Add(3400 * time.Millisecond),
		}

		if got := timing.FirstResponseLatency(); got != 1500*time.Millisecond {
			t.Errorf("ResponseTiming.FirstResponseLatency() = %v, want %v", got, 1500*time.Millisecond)
		}
		if got := timing.TotalResponseLatency(); got != 3400*time.Millisecond {
			t.Errorf("ResponseTiming.TotalResponseLatency() = %v, want %v", got, 3400*time.Millisecond)
		}
		if got := timing.FirstResponseLatencyMs(); got != 1500 {
			t.Errorf("ResponseTiming.FirstResponseLatencyMs() = %d, want 1500", got)
		}
		if got := timing.TotalResponseLatencyMs(); got != 3400 {
			t.Errorf("ResponseTiming.TotalResponseLatencyMs() = %d, want 3400", got)
		}
		if got := timing.Latency(); got != (ResponseLatency{FirstResponseLatencyMs: 1500, TotalResponseLatencyMs: 3400}) {
			t.Errorf("ResponseTiming.Latency() = %+v, want first=1500 total=3400", got)
		}
	})

	t.Run("first response latency is zero until the first chunk arrives", func(t *testing.T) {
		t.Parallel()

		timing := ResponseTiming{
			RequestReceivedAt: base,
			LastResponseAt:    base.Add(1200 * time.Millisecond),
		}
		if got := timing.FirstResponseLatencyMs(); got != 0 {
			t.Errorf("ResponseTiming.FirstResponseLatencyMs() = %d, want 0 when first response is missing", got)
		}
	})
}
