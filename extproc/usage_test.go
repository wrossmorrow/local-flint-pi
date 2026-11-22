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

func TestParseUsageFromSSE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		want    *ResponseUsage
		wantErr bool
	}{
		{
			name: "sse data event with usage",
			body: "data: " + `{"id": "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11", "object": "chat.completion", "model": "flint", "usage": {"prompt_tokens": 3, "completion_tokens": 8, "total_tokens": 11, "prompt_tokens_details": {"cached_tokens": 0}}}` + "\n\n",
			want: &ResponseUsage{
				ID:    "chatcmpl-68430e06-329a-47fa-ac4c-c6b74a830b11",
				Model: "flint",
				Usage: Usage{
					PromptTokens:        3,
					CompletionTokens:    8,
					TotalTokens:         11,
					PromptTokensDetails: PromptTokensDetails{CachedTokens: 0},
				},
			},
		},
		{
			name: "sse multiple data events, last carries usage",
			body: "data: " + `{"id": "chatcmpl-aaa", "object": "chat.completion", "model": "flint"}` + "\n\n" +
				"data: " + `{"id": "chatcmpl-bbb", "object": "chat.completion", "model": "flint", "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7, "prompt_tokens_details": {"cached_tokens": 1}}}` + "\n\n",
			want: &ResponseUsage{
				ID:    "chatcmpl-bbb",
				Model: "flint",
				Usage: Usage{
					PromptTokens:        5,
					CompletionTokens:    2,
					TotalTokens:         7,
					PromptTokensDetails: PromptTokensDetails{CachedTokens: 1},
				},
			},
		},
		{
			name: "sse llama-server/vllm stream with DONE trailer and omitted cached details",
			body: "data: " + `{"id": "chatcmpl-ink-1", "object": "chat.completion.chunk", "model": "thinkingmachines/Inkling-2-Mini", "choices": [{"index": 0, "delta": {"content": "hi"}}]}` + "\n\n" +
				"data: " + `{"id": "chatcmpl-ink-1", "object": "chat.completion.chunk", "model": "thinkingmachines/Inkling-2-Mini", "choices": [], "usage": {"prompt_tokens": 21, "completion_tokens": 9, "total_tokens": 30}}` + "\n\n" +
				"data: [DONE]\n\n",
			want: &ResponseUsage{
				ID:    "chatcmpl-ink-1",
				Model: "thinkingmachines/Inkling-2-Mini",
				Usage: Usage{
					PromptTokens:     21,
					CompletionTokens: 9,
					TotalTokens:      30,
				},
			},
		},
		{
			name:    "sse data event without usage",
			body:    "data: " + `{"object": "chat.completion", "model": "flint"}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "sse no data events",
			body:    "\n\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseUsageFromSSE(tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseUsageFromSSE() expected error, got nil (usage=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUsageFromSSE() unexpected error: %v", err)
			}
			if got.ID != tt.want.ID {
				t.Fatalf("ParseUsageFromSSE() ID = %q, want %q", got.ID, tt.want.ID)
			}
			if got.Model != tt.want.Model {
				t.Fatalf("ParseUsageFromSSE() Model = %q, want %q", got.Model, tt.want.Model)
			}
			if got.Usage != tt.want.Usage {
				t.Fatalf("ParseUsageFromSSE() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
