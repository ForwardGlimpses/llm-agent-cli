package llm

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openai/openai-go"
)

func mustChunk(t *testing.T, raw string) openai.ChatCompletionChunk {
	t.Helper()
	var c openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	return c
}

func envelope(delta string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"mock",` +
		`"choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

func TestAccumulatorAssemblesToolCallFragmentsByIndex(t *testing.T) {
	acc := NewAccumulator()
	chunks := []string{
		envelope(`{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_current_time","arguments":""}}]}`),
		envelope(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"time"}}]}`),
		envelope(`{"tool_calls":[{"index":0,"function":{"arguments":"zone\":\"Asia"}}]}`),
		envelope(`{"tool_calls":[{"index":0,"function":{"arguments":"/Shanghai\"}"}}]}`),
	}
	for _, raw := range chunks {
		acc.Consume(mustChunk(t, raw), nil)
	}
	res := acc.Result()
	if len(res.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(res.ToolCalls))
	}
	call := res.ToolCalls[0]
	if call.ID != "call_abc" {
		t.Errorf("id = %q", call.ID)
	}
	if call.Name != "get_current_time" {
		t.Errorf("name = %q", call.Name)
	}
	want := `{"timezone":"Asia/Shanghai"}`
	if call.Arguments != want {
		t.Errorf("arguments = %q, want %q", call.Arguments, want)
	}
	if !json.Valid([]byte(call.Arguments)) {
		t.Errorf("assembled arguments are not valid JSON: %s", call.Arguments)
	}
}

func TestAccumulatorKeepsParallelToolCallsSeparated(t *testing.T) {
	acc := NewAccumulator()
	raw := []string{
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[` +
			`{"index":0,"id":"call_a","type":"function","function":{"name":"calc","arguments":"{\"expr\":"}},` +
			`{"index":1,"id":"call_b","type":"function","function":{"name":"clock","arguments":"{\"tz\":"}}]}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[` +
			`{"index":1,"function":{"arguments":"\"UTC\"}"}},` +
			`{"index":0,"function":{"arguments":"\"1+2\"}"}}]}}]}`,
	}
	for _, r := range raw {
		acc.Consume(mustChunk(t, r), nil)
	}
	res := acc.Result()
	if len(res.ToolCalls) != 2 {
		t.Fatalf("got %d calls", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "calc" || res.ToolCalls[0].Arguments != `{"expr":"1+2"}` {
		t.Errorf("call 0 = %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].Name != "clock" || res.ToolCalls[1].Arguments != `{"tz":"UTC"}` {
		t.Errorf("call 1 = %+v", res.ToolCalls[1])
	}
}

func TestAccumulatorTextReasoningAndDeepSeekUsage(t *testing.T) {
	acc := NewAccumulator()
	chunks := []string{
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"你","reasoning_content":"想一想"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36}}`,
	}
	var text string
	for _, raw := range chunks {
		acc.Consume(mustChunk(t, raw), func(d Delta) { text += d.Text })
	}
	res := acc.Result()
	if res.Content != "你好" || text != "你好" {
		t.Errorf("content = %q, streamed = %q", res.Content, text)
	}
	if res.Reasoning != "想一想" {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
	if res.FinishReason != "stop" {
		t.Errorf("finish = %q", res.FinishReason)
	}
	if res.Usage.Prompt != 100 || res.Usage.Completion != 20 || res.Usage.Total != 120 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Usage.Cached != 64 || res.Usage.CacheMissed != 36 {
		t.Errorf("cache usage = %+v", res.Usage)
	}
}

func TestAccumulatorToleratesCumulativeToolName(t *testing.T) {
	acc := NewAccumulator()
	base := `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[%s]},"finish_reason":null}]}`
	parts := []string{
		`{"index":0,"id":"c","type":"function","function":{"name":"get_","arguments":"{}"}}`,
		`{"index":0,"function":{"name":"get_current"}}`,
		`{"index":0,"function":{"name":"get_current_time"}}`,
	}
	for _, p := range parts {
		acc.Consume(mustChunk(t, fmt.Sprintf(base, p)), nil)
	}
	if got := acc.Result().ToolCalls[0].Name; got != "get_current_time" {
		t.Fatalf("name = %q", got)
	}
}
