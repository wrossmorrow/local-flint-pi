package main

import (
	"testing"
)

func TestParseUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		want    *Usage
		wantErr bool
	}{
		{
			name: "full chat completion response with usage",
			body: `{"id": "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11", "system_fingerprint": "flint-int4", "object": "chat.completion", "model": "flint", "created": 1792981796, "choices": [{"index": 0, "finish_reason": "length", "message": {"role": "assistant", "reasoning": "Hmm, the user just said \""}}], "usage": {"prompt_tokens": 3, "completion_tokens": 8, "total_tokens": 11, "prompt_tokens_details": {"cached_tokens": 0}}}`,
			want: &Usage{
				PromptTokens:        3,
				CompletionTokens:    8,
				TotalTokens:         11,
				PromptTokensDetails: PromptTokensDetails{CachedTokens: 0},
			},
		},
		{
			name: "usage with cached tokens",
			body: `{"object": "chat.completion", "model": "flint", "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17, "prompt_tokens_details": {"cached_tokens": 9}}}`,
			want: &Usage{
				PromptTokens:        12,
				CompletionTokens:    5,
				TotalTokens:         17,
				PromptTokensDetails: PromptTokensDetails{CachedTokens: 9},
			},
		},
		{
			name:    "response without usage",
			body:    `{"object": "chat.completion", "model": "flint", "choices": []}`,
			wantErr: true,
		},
		{
			name:    "malformed json",
			body:    `{"object": "chat.completion", "usage":`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseUsage([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseUsage() expected error, got nil (usage=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUsage() unexpected error: %v", err)
			}
			if *got != *tt.want {
				t.Fatalf("ParseUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
