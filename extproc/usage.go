package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PromptTokensDetails carries sub-token accounting reported by the model.
type PromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// Usage holds the token accounting reported by a model in its response, plus
// the session attribution captured for the request via the x-session-id and
// x-session-name headers.
type Usage struct {
	SessionID           string              `json:"session_id,omitempty"`
	SessionName         string              `json:"session_name,omitempty"`
	PromptTokens        int64               `json:"prompt_tokens"`
	CompletionTokens    int64               `json:"completion_tokens"`
	TotalTokens         int64               `json:"total_tokens"`
	PromptTokensDetails PromptTokensDetails `json:"prompt_tokens_details"`
}

// ResponseUsage pairs a chat completion response id with its token usage.
type ResponseUsage struct {
	ID    string `json:"id"`
	Usage Usage  `json:"usage"`
}

// responseUsageEnvelope mirrors the single-JSON-doc shape of a chat completion
// response carrying an OpenAI-style "usage" field.
type responseUsageEnvelope struct {
	ID    string `json:"id"`
	Usage Usage  `json:"usage"`
}

// parseResponseUsage unmarshals a complete JSON chat completion response and
// returns the response id together with its token usage. An error is returned
// if the body is not valid JSON or carries no usage data.
func parseResponseUsage(body []byte) (*ResponseUsage, error) {
	var env responseUsageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response JSON: %w", err)
	}
	if env.Usage.TotalTokens == 0 && env.Usage.CompletionTokens == 0 && env.Usage.PromptTokens == 0 {
		return nil, errors.New("response body contained no usage data")
	}
	return &ResponseUsage{ID: env.ID, Usage: env.Usage}, nil
}

// ParseUsage extracts token usage from a complete JSON response body. An
// error is returned if the body is not valid JSON or carries no usage data.
func ParseUsage(body []byte) (*Usage, error) {
	r, err := parseResponseUsage(body)
	if err != nil {
		return nil, err
	}
	return &r.Usage, nil
}

// ParseUsageFromSSE extracts token usage from a Server-Sent Events (SSE)
// formatted response body. Data events carry JSON payloads prefixed with
// "data:". The usage is taken from the last data event that carries usage.
func ParseUsageFromSSE(body string) (*ResponseUsage, error) {
	var last *ResponseUsage
	for _, event := range sseEvents(body) {
		data := strings.Join(event, "\n")
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		r, err := parseResponseUsage([]byte(data))
		if err != nil {
			continue
		}
		last = r
	}
	if last == nil {
		return nil, errors.New("no usage found in SSE response body")
	}
	return last, nil
}

// sseEvents splits an SSE body into events, each a list of data field values
// (multiple data lines within one event are joined with a newline, per the SSE
// spec).
func sseEvents(body string) [][]string {
	var events [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			events = append(events, cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			cur = append(cur, strings.TrimPrefix(line, "data:"))
		}
	}
	flush()
	return events
}
