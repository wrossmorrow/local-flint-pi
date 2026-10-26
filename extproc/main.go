// Command extproc runs an Envoy external processor (extproc) gRPC server for
// the local-flint-pi model proxy setup, using the envoy-extproc-sdk-go.
//
// The processor starts as a no-op: it continues every request phase through
// Envoy untouched. The next step is to accumulate streamed response bodies,
// parse the JSON responses, and extract request token counts (usage) to feed
// metrics/logging for the model calls proxied through Envoy.
package main

import (
	"log"
	"log/slog"
	"os"

	ep "github.com/wrossmorrow/envoy-extproc-sdk-go"
)

// tokenCounterRequestProcessor is the Envoy external processor implementation.
//
// Phase handling is intentionally a no-op for now: every phase continues the
// request and response streams untouched, and errors are logged. Once the
// token counting is wired up, the phases will carry the state needed to
// accumulate streamed request/response bodies and extract JSON token usage.
type tokenCounterRequestProcessor struct {
	opts *ep.ProcessingOptions
}

func (s *tokenCounterRequestProcessor) GetName() string {
	return "token-counter"
}

func (s *tokenCounterRequestProcessor) GetOptions() *ep.ProcessingOptions {
	return s.opts
}

func (s *tokenCounterRequestProcessor) ProcessRequestHeaders(ctx *ep.RequestContext, headers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessRequestBody(ctx *ep.RequestContext, body []byte) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessRequestTrailers(ctx *ep.RequestContext, trailers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessResponseHeaders(ctx *ep.RequestContext, headers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessResponseBody(ctx *ep.RequestContext, body []byte) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ProcessResponseTrailers(ctx *ep.RequestContext, trailers ep.AllHeaders) error {
	return ctx.ContinueRequest()
}

func (s *tokenCounterRequestProcessor) ErrorHandler(ctx *ep.RequestContext, phase int, err error) {
	log.Printf("Error in phase %s: %v", ep.RequestPhaseToString(phase), err)
}

func (s *tokenCounterRequestProcessor) Close(gracePeriodSeconds int32) error {
	log.Printf("Closing %s", s.GetName())
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	proc := &tokenCounterRequestProcessor{}
	if err := proc.Init(ep.NewDefaultProcessingOptions(), nil); err != nil {
		log.Fatalf("Initialize the processor failed: %v.", err.Error())
	}

	ep.MustServe(sopts, proc, logger)
}
