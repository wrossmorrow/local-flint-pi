// Command extproc runs an Envoy external processor (extproc) gRPC server for
// the local-flint-pi model proxy setup, using the envoy-extproc-sdk-go.
//
// The processor continues request phases through Envoy untouched. For
// responses, it accumulates streamed body chunks in the per-stream request
// context, and once the response stream ends, finalizes response timing and
// parses the JSON body to extract token usage for the model calls proxied
// through Envoy.
package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	ep "github.com/wrossmorrow/envoy-extproc-sdk-go"
)

// accumulator keys used to stash per-stream state in the request context.
const (
	responseBodyKey   = "responseBodyBuf"
	responseUsageKey  = "responseUsage"
	responseTimingKey = "responseTiming"
)

// tokenCounterRequestProcessor is the Envoy external processor implementation.
//
// Request phases continue the stream untouched. Response bodies are
// accumulated in the per-stream context and, once the response stream ends,
// the JSON body is parsed to extract token usage.
type tokenCounterRequestProcessor struct {
	opts   *ep.ProcessingOptions
	logger *slog.Logger
	store  *UsageStore
}

func (s *tokenCounterRequestProcessor) GetName() string {
	return "token-counter"
}

func (s *tokenCounterRequestProcessor) GetOptions() *ep.ProcessingOptions {
	return s.opts
}

func (s *tokenCounterRequestProcessor) ProcessRequestHeaders(ctx *ep.RequestContext, headers ep.AllHeaders) error {
	s.logger.Info("request received",
		slog.String("method", ctx.Method),
		slog.String("path", ctx.FullPath),
	)

	// stamp the request-received timestamp; the SDK seeds the request context
	// with the stream start time at the beginning of the request headers phase,
	// so prefer it and fall back to the current time if the phase was skipped
	timing := &ResponseTiming{RequestReceivedAt: ctx.Started}
	if timing.RequestReceivedAt.IsZero() {
		timing.RequestReceivedAt = time.Now()
	}
	if err := ctx.SetValue(responseTimingKey, timing); err != nil {
		return err
	}
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessRequestBody(ctx *ep.RequestContext, body []byte) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessRequestTrailers(ctx *ep.RequestContext, trailers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessResponseHeaders(ctx *ep.RequestContext, headers ep.AllHeaders) error {
	// reset the response accumulators for this stream's response
	if err := ctx.SetValue(responseBodyKey, ""); err != nil {
		return err
	}
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessResponseBody(ctx *ep.RequestContext, body []byte) error {
	buf, err := ctx.GetValue(responseBodyKey)
	if err != nil {
		return err
	}
	acc, ok := buf.(string)
	if !ok {
		acc = ""
	}
	acc += string(body)
	if err := ctx.SetValue(responseBodyKey, acc); err != nil {
		return err
	}

	timing, err := ctx.GetValue(responseTimingKey)
	if err != nil {
		return err
	}
	t, ok := timing.(*ResponseTiming)
	if !ok {
		return fmt.Errorf("expected *ResponseTiming in request context, got %T", timing)
	}

	// stamp the first response timestamp on the first non-empty body chunk
	// received, so the measurement is a valid time-to-first-token style latency
	if t.FirstResponseAt.IsZero() && len(body) > 0 {
		t.FirstResponseAt = time.Now()
	}

	if ctx.EndOfStream {
		if err := s.finalizeResponse(ctx, acc, t); err != nil {
			return err
		}
	}

	return ctx.ContinueRequest()
}

// finalizeResponse finalizes the response timing once the response stream has
// ended, parses the accumulated response body for token usage, and writes the
// usage record. Failures to parse are logged and do not fail the stream.
func (s *tokenCounterRequestProcessor) finalizeResponse(ctx *ep.RequestContext, body string, timing *ResponseTiming) error {
	// the entire response body has now been received
	timing.LastResponseAt = time.Now()

	status := "ok"
	latency := timing.Latency()
	s.logger.Info("response timing recorded",
		slog.Time("request_received_at", timing.RequestReceivedAt),
		slog.Time("first_response_at", timing.FirstResponseAt),
		slog.Time("last_response_at", timing.LastResponseAt),
		slog.Int64("first_response_latency_ms", latency.FirstResponseLatencyMs),
		slog.Int64("total_response_latency_ms", latency.TotalResponseLatencyMs),
	)

	usage, err := ParseUsageFromSSE(body)
	if err != nil {
		s.logger.Warn("failed to parse response body for token usage", slog.String("error", err.Error()))
		return nil
	}
	if err := ctx.SetValue(responseUsageKey, &usage.Usage); err != nil {
		return err
	}
	if err := s.store.Record(&usage.Usage, usage.ID, status, *timing, latency); err != nil {
		s.logger.Warn("failed to write token usage to wal", slog.String("error", err.Error()))
	}
	s.logger.Info("token usage extracted",
		slog.Int64("prompt_tokens", usage.Usage.PromptTokens),
		slog.Int64("completion_tokens", usage.Usage.CompletionTokens),
		slog.Int64("total_tokens", usage.Usage.TotalTokens),
		slog.Int64("cached_tokens", usage.Usage.PromptTokensDetails.CachedTokens),
	)
	return nil
}

func (s *tokenCounterRequestProcessor) ProcessResponseTrailers(ctx *ep.RequestContext, trailers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ErrorHandler(ctx *ep.RequestContext, phase int, err error) {
	s.logger.Error("error in phase", slog.String("phase", ep.RequestPhaseToString(phase)), slog.String("error", err.Error()))

	// mark the per-stream timing as errored; the finalized-with-error record
	// is written once the error has been handled
	timing, terr := ctx.GetValue(responseTimingKey)
	if terr != nil {
		return
	}
	t, ok := timing.(*ResponseTiming)
	if !ok {
		return
	}
	t.ErrorAt = time.Now()
}

func (s *tokenCounterRequestProcessor) Close(gracePeriodSeconds int32) error {
	s.logger.Info("closing processor", slog.String("name", s.GetName()))
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

func (s *tokenCounterRequestProcessor) Init(opts *ep.ProcessingOptions, nonFlagArgs []string) error {
	s.opts = opts
	return nil
}

// processor is the SDK's processor interface plus the lifecycle hook used to
// seed the processing options before the server starts.
type processor interface {
	Init(opts *ep.ProcessingOptions, nonFlagArgs []string) error

	ep.RequestProcessor
}

func main() {
	sopts := ep.NewDefaultServerOptions()
	sopts.ExtProcPort = 50051
	sopts.MetricsHTTPPort = 9090
	sopts.TerminationGracePeriodSeconds = 30
	sopts.UnreadyPropagationDelaySeconds = 0

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	usageWALPath := os.Getenv("EXTOPROC_USAGE_WAL")
	if usageWALPath == "" {
		usageWALPath = "/tmp/extproc-usage.jsonl"
	}
	store, err := NewUsageStore(usageWALPath)
	if err != nil {
		log.Fatalf("failed to open usage wal %q: %v", usageWALPath, err)
	}

	proc := &tokenCounterRequestProcessor{logger: logger, store: store}
	if err := proc.Init(ep.NewDefaultProcessingOptions(), nil); err != nil {
		log.Fatalf("Initialize the processor failed: %v.", err.Error())
	}

	ep.MustServe(sopts, proc, logger)
}
