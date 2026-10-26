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

// responseUsageEnvelope mirrors the single-JSON-object shape of a chat
// completion response carrying an OpenAI-style "usage" field, as emitted by
// the flint model responses.
type responseUsageEnvelope struct {
	Usage Usage `json:"usage"`
}

// ParseUsage extracts token usage from a complete JSON response body. It
// expects a single JSON object with a top-level "usage" field. An error is
// returned if the body is not valid JSON or carries no usage data.
func ParseUsage(body []byte) (*Usage, error) {
	var env responseUsageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response JSON: %w", err)
	}

	if env.Usage.TotalTokens == 0 && env.Usage.CompletionTokens == 0 && env.Usage.PromptTokens == 0 {
		return nil, errors.New("response body contained no usage data")
	}

	return &env.Usage, nil
}
