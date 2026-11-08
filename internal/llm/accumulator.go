package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/respjson"
)

// Delta is a single incremental update extracted from an SSE chunk.
type Delta struct {
	Text      string
	Reasoning string
}

type toolCallBuf struct {
	index int64
	id    string
	name  string
	typ   string
	args  strings.Builder
}

// Accumulator assembles a streamed chat completion.
//
// This is the part interviewers usually probe: in SSE streaming a tool call is
// not delivered as one object. The first fragment carries index/id/name and
// later fragments carry only index plus a slice of the JSON argument text.
// Fragments for different calls may be interleaved, so the only correct way to
// rebuild them is to key by index and concatenate argument strings in arrival
// order.
type Accumulator struct {
	text      strings.Builder
	reasoning strings.Builder
	calls     []*toolCallBuf
	byIndex   map[int64]*toolCallBuf
	usage     Tokens
	finish    string
	model     string
}

// NewAccumulator returns an empty accumulator.
func NewAccumulator() *Accumulator {
	return &Accumulator{byIndex: make(map[int64]*toolCallBuf)}
}

// Consume folds one chunk into the accumulator and invokes onDelta for visible
// text. onDelta may be nil.
func (a *Accumulator) Consume(chunk openai.ChatCompletionChunk, onDelta func(Delta)) {
	if chunk.Model != "" {
		a.model = chunk.Model
	}
	if chunk.JSON.Usage.Valid() {
		a.applyUsage(chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Content != "" {
			a.text.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(Delta{Text: delta.Content})
			}
		}
		if r := extraString(delta.JSON.ExtraFields, "reasoning_content", "reasoning"); r != "" {
			a.reasoning.WriteString(r)
			if onDelta != nil {
				onDelta(Delta{Reasoning: r})
			}
		}
		for _, tc := range delta.ToolCalls {
			a.mergeToolCall(tc)
		}
		if choice.FinishReason != "" {
			a.finish = choice.FinishReason
		}
	}
}

func (a *Accumulator) mergeToolCall(tc openai.ChatCompletionChunkChoiceDeltaToolCall) {
	buf, ok := a.byIndex[tc.Index]
	if !ok {
		buf = &toolCallBuf{index: tc.Index}
		a.byIndex[tc.Index] = buf
		a.calls = append(a.calls, buf)
	}
	if tc.ID != "" {
		buf.id = tc.ID
	}
	if tc.Type != "" {
		buf.typ = tc.Type
	}
	buf.name = mergeName(buf.name, tc.Function.Name)
	if arg := tc.Function.Arguments; arg != "" {
		buf.args.WriteString(arg)
	}
}

// mergeName tolerates both spec-compliant providers (name only in the first
// fragment) and a few gateways that resend the accumulated name on every chunk.
func mergeName(current, fragment string) string {
	if fragment == "" {
		return current
	}
	switch {
	case current == "":
		return fragment
	case fragment == current:
		return current
	case strings.HasPrefix(fragment, current):
		return fragment // cumulative form
	case strings.HasPrefix(current, fragment):
		return current // repeated prefix
	default:
		return current + fragment
	}
}

func (a *Accumulator) applyUsage(u openai.CompletionUsage) {
	u2 := Tokens{
		Prompt:     u.PromptTokens,
		Completion: u.CompletionTokens,
		Total:      u.TotalTokens,
		Cached:     u.PromptTokensDetails.CachedTokens,
		Reasoning:  u.CompletionTokensDetails.ReasoningTokens,
	}
	if v, ok := extraInt(u.JSON.ExtraFields, "prompt_cache_hit_tokens"); ok {
		u2.Cached = v
	}
	if v, ok := extraInt(u.JSON.ExtraFields, "prompt_cache_miss_tokens"); ok {
		u2.CacheMissed = v
	}
	if u2.Total == 0 {
		u2.Total = u2.Prompt + u2.Completion
	}
	// Some gateways send several usage chunks; the last one wins because it is
	// the authoritative total for the request.
	a.usage = u2
}

// Result returns a snapshot. Tool calls are ordered by their stream index.
func (a *Accumulator) Result() *Result {
	sort.SliceStable(a.calls, func(i, j int) bool { return a.calls[i].index < a.calls[j].index })
	calls := make([]ToolCall, 0, len(a.calls))
	for i, buf := range a.calls {
		id := buf.id
		if id == "" {
			// Defensive: providers are supposed to send an id, but never let a
			// malformed stream produce an empty tool_call_id (the follow-up tool
			// message would be rejected by the API).
			id = fmt.Sprintf("call_%d", i)
		}
		args := buf.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		calls = append(calls, ToolCall{ID: id, Name: buf.name, Arguments: args})
	}
	return &Result{
		Content:      a.text.String(),
		Reasoning:    a.reasoning.String(),
		ToolCalls:    calls,
		FinishReason: a.finish,
		Model:        a.model,
		Usage:        a.usage,
	}
}

func extraString(fields map[string]respjson.Field, names ...string) string {
	for _, name := range names {
		f, ok := fields[name]
		if !ok {
			continue
		}
		// ExtraFields are not "valid" in respjson's sense (they are undeclared
		// by the OpenAPI schema); the raw JSON is still usable.
		raw := strings.TrimSpace(f.Raw())
		if raw == "" || raw == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
	}
	return ""
}

func extraInt(fields map[string]respjson.Field, name string) (int64, bool) {
	f, ok := fields[name]
	if !ok {
		return 0, false
	}
	raw := strings.TrimSpace(f.Raw())
	if raw == "" || raw == "null" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
