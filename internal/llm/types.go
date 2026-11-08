// Package llm is a thin, explicit wrapper around the official openai-go SDK.
//
// It owns the parts a production CLI needs on top of the SDK: SSE delta
// accumulation (text + tool calls), retries with exponential backoff, per
// request timeouts, usage extraction and error classification with actionable
// hints. It deliberately contains no agent logic.
package llm

import (
	"time"

	"github.com/openai/openai-go"
)

// ToolCall is a fully assembled tool call. Arguments is the raw JSON string
// produced by the model; it is intentionally not decoded here because the
// caller owns the argument schema.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Tokens is a provider-neutral token accounting snapshot.
//
// Cached tokens are a subset of Prompt tokens (OpenAI-compatible semantics).
// Reasoning tokens are a subset of Completion tokens.
type Tokens struct {
	Prompt      int64
	Completion  int64
	Total       int64
	Cached      int64
	Reasoning   int64
	CacheMissed int64 // optional: DeepSeek's prompt_cache_miss_tokens
}

// Add accumulates v into t.
func (t *Tokens) Add(v Tokens) {
	t.Prompt += v.Prompt
	t.Completion += v.Completion
	t.Total += v.Total
	t.Cached += v.Cached
	t.Reasoning += v.Reasoning
	t.CacheMissed += v.CacheMissed
}

// Result is the outcome of one logical streaming request, after retries.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Model        string
	Usage        Tokens
	Attempts     int
	Elapsed      time.Duration
	// Started is true when at least one visible delta (text or reasoning) was
	// emitted to the caller. Once that happens a retry would duplicate output,
	// so the client stops retrying and returns the partial result with the error.
	Started bool
}

// RetryInfo describes a retry that is about to happen.
type RetryInfo struct {
	Attempt     int // 1-based number of the attempt that just failed
	MaxAttempts int
	Delay       time.Duration
	Reason      string
	Status      int
}

// StreamOptions is one streaming chat request.
type StreamOptions struct {
	Messages       []openai.ChatCompletionMessageParamUnion
	Tools          []openai.ChatCompletionToolParam
	ResponseFormat *openai.ChatCompletionNewParamsResponseFormatUnion
	Model          string

	OnText      func(string)
	OnReasoning func(string)
	OnRetry     func(RetryInfo)
}
