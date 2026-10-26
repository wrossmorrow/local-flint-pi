package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// PromptTokensDetails carries sub-token accounting reported by the model.
type PromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// Usage holds the token accounting reported by a model in its response.
type Usage struct {
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
