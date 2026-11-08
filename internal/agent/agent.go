// Package agent implements the tool-calling loop on top of the llm client.
//
// One user turn may take several model requests: the model asks for a tool, we
// execute it, append the result as a tool message, and call the model again
// until it returns a normal assistant message (or we hit MaxTurns).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

	"agentcli/internal/llm"
	"agentcli/internal/schema"
	"agentcli/internal/tools"
	"agentcli/internal/usage"
)

const defaultSystemPrompt = "You are a careful AI assistant running in a terminal. " +
	"Answer in the user's language, be concise, and use the provided tools when they make the answer more accurate. " +
	"Never invent tool results."

// Hooks are optional UI callbacks. They are invoked synchronously from the
// request goroutine; do not block in them.
type Hooks struct {
	OnText      func(string)
	OnReasoning func(string)
	OnRetry     func(llm.RetryInfo)
	OnRequest   func(RequestInfo)
	OnToolStart func(name, args string)
	OnToolEnd   func(name, output string, err error, elapsed time.Duration)
	OnRepair    func(errors []string, attempt int)
}

// Options configures an Agent.
type Options struct {
	System   string
	MaxTurns int
	// MaxRepairs is the number of extra requests allowed after a schema
	// validation failure. 0 disables repair; the CLI defaults it to 2.
	MaxRepairs int
	Schema     *schema.Schema

	// ResponseFormat is sent to the API when Schema is set. It is built by the
	// CLI because support differs between OpenAI (json_schema) and DeepSeek
	// (json_object).
	ResponseFormat *openai.ChatCompletionNewParamsResponseFormatUnion

	ToolTimeout time.Duration
	Hooks       Hooks
}

func (o Options) withDefaults() Options {
	if o.MaxTurns <= 0 {
		o.MaxTurns = 8
	}
	if o.MaxRepairs < 0 {
		o.MaxRepairs = 0
	}
	if o.ToolTimeout <= 0 {
		o.ToolTimeout = 30 * time.Second
	}
	return o
}

// RequestInfo describes one completed (or failed) model request.
type RequestInfo struct {
	Tokens       usage.Tokens
	Model        string
	Attempts     int
	FinishReason string
	ToolCalls    int
	Elapsed      time.Duration
}

// TurnResult is the outcome of one user turn.
type TurnResult struct {
	Content      string
	Reasoning    string
	Structured   json.RawMessage
	FinishReason string
	Stats        usage.Stats // delta for this turn
	Requests     int
	ToolCalls    int
	Repairs      int
	Duration     time.Duration
}

// MaxTurnsError means the model kept asking for tools and we stopped.
type MaxTurnsError struct {
	Max int
}

func (e *MaxTurnsError) Error() string {
	return fmt.Sprintf("stopped after %d model requests: the model may be stuck in a tool loop", e.Max)
}

// StructuredError means the model never produced schema-valid JSON.
type StructuredError struct {
	Attempts int
	Errors   []string
	Raw      string
}

func (e *StructuredError) Error() string {
	return fmt.Sprintf("structured output failed after %d attempts: %s", e.Attempts, strings.Join(e.Errors, "; "))
}

// Agent owns conversation state.
type Agent struct {
	client   *llm.Client
	tools    *tools.Registry
	opts     Options
	messages []openai.ChatCompletionMessageParamUnion
	total    usage.Stats
}

// New creates an agent with a fresh conversation.
func New(client *llm.Client, registry *tools.Registry, opts Options) *Agent {
	a := &Agent{client: client, tools: registry, opts: opts.withDefaults()}
	a.Reset()
	return a
}

// Reset clears the conversation and session counters while keeping the system
// prompt and tool configuration.
func (a *Agent) Reset() {
	a.messages = nil
	a.total = usage.Stats{}
	if prompt := a.buildSystemPrompt(); prompt != "" {
		a.messages = append(a.messages, openai.SystemMessage(prompt))
	}
}

// Total returns session-level accounting.
func (a *Agent) Total() usage.Stats { return a.total }

// MessageCount returns the number of messages currently in the context.
func (a *Agent) MessageCount() int { return len(a.messages) }

// Options returns the effective options.
func (a *Agent) Options() Options { return a.opts }

// Run executes one user turn. It streams text through Hooks.OnText as it
// arrives. On error the returned TurnResult still contains partial output and
// accounting for the requests that succeeded.
func (a *Agent) Run(ctx context.Context, input string) (*TurnResult, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("empty input")
	}
	before := a.total
	start := time.Now()
	turn := &TurnResult{}
	a.messages = append(a.messages, openai.UserMessage(input))

	for step := 0; step < a.opts.MaxTurns; step++ {
		res, err := a.client.Stream(ctx, llm.StreamOptions{
			Messages:       a.messages,
			Tools:          a.toolParams(),
			ResponseFormat: a.opts.ResponseFormat,
			OnText:         a.opts.Hooks.OnText,
			OnReasoning:    a.opts.Hooks.OnReasoning,
			OnRetry:        a.opts.Hooks.OnRetry,
		})
		if res != nil {
			tok := usage.FromLLM(res.Usage)
			a.total.AddTokens(tok)
			turn.Requests++
			if a.opts.Hooks.OnRequest != nil {
				a.opts.Hooks.OnRequest(RequestInfo{
					Tokens:       tok,
					Model:        res.Model,
					Attempts:     res.Attempts,
					FinishReason: res.FinishReason,
					ToolCalls:    len(res.ToolCalls),
					Elapsed:      res.Elapsed,
				})
			}
		}
		if err != nil {
			if res != nil {
				turn.Content = res.Content
				turn.Reasoning = res.Reasoning
			}
			turn.Duration = time.Since(start)
			turn.Stats = a.total.Delta(before)
			return turn, err
		}

		a.messages = append(a.messages, assistantParam(res))

		if len(res.ToolCalls) > 0 {
			for _, call := range res.ToolCalls {
				output := a.runTool(ctx, call)
				turn.ToolCalls++
				a.total.ToolCalls++
				a.messages = append(a.messages, openai.ToolMessage(output, call.ID))
			}
			continue
		}

		turn.Content = res.Content
		turn.Reasoning = res.Reasoning
		turn.FinishReason = res.FinishReason

		if a.opts.Schema != nil {
			raw, validationErrs := a.checkStructured(res.Content)
			if len(validationErrs) > 0 {
				if turn.Repairs >= a.opts.MaxRepairs {
					turn.Duration = time.Since(start)
					turn.Stats = a.total.Delta(before)
					return turn, &StructuredError{
						Attempts: turn.Repairs + 1,
						Errors:   validationErrs,
						Raw:      res.Content,
					}
				}
				turn.Repairs++
				a.total.Repairs++
				if a.opts.Hooks.OnRepair != nil {
					a.opts.Hooks.OnRepair(validationErrs, turn.Repairs)
				}
				a.messages = append(a.messages, openai.UserMessage(repairPrompt(validationErrs)))
				continue
			}
			turn.Structured = raw
		}

		turn.Duration = time.Since(start)
		turn.Stats = a.total.Delta(before)
		return turn, nil
	}

	turn.Duration = time.Since(start)
	turn.Stats = a.total.Delta(before)
	return turn, &MaxTurnsError{Max: a.opts.MaxTurns}
}

func (a *Agent) runTool(ctx context.Context, call llm.ToolCall) string {
	if a.opts.Hooks.OnToolStart != nil {
		a.opts.Hooks.OnToolStart(call.Name, call.Arguments)
	}
	start := time.Now()
	var (
		out string
		err error
	)
	if a.tools == nil {
		err = errors.New("no tools are registered")
	} else {
		tctx := ctx
		var cancel context.CancelFunc
		if a.opts.ToolTimeout > 0 {
			tctx, cancel = context.WithTimeout(ctx, a.opts.ToolTimeout)
		}
		out, err = a.tools.Execute(tctx, call.Name, json.RawMessage(call.Arguments))
		if cancel != nil {
			cancel()
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			out = "ERROR: tool execution was cancelled or timed out: " + err.Error()
		} else {
			out = "ERROR: " + err.Error()
		}
	}
	if strings.TrimSpace(out) == "" {
		out = "(tool returned no output)"
	}
	if a.opts.Hooks.OnToolEnd != nil {
		a.opts.Hooks.OnToolEnd(call.Name, out, err, time.Since(start))
	}
	return out
}

func (a *Agent) checkStructured(content string) (json.RawMessage, []string) {
	raw, err := schema.ExtractJSON(content)
	if err != nil {
		return nil, []string{"output is not JSON: " + err.Error()}
	}
	if _, err := a.opts.Schema.ValidateJSON(raw); err != nil {
		var ve *schema.ValidationErrors
		if errors.As(err, &ve) {
			return raw, ve.Errors
		}
		return raw, []string{err.Error()}
	}
	return raw, nil
}

func (a *Agent) toolParams() []openai.ChatCompletionToolParam {
	if a.tools == nil {
		return nil
	}
	defs := a.tools.All()
	out := make([]openai.ChatCompletionToolParam, 0, len(defs))
	for _, def := range defs {
		out = append(out, openai.ChatCompletionToolParam{
			Type: "function",
			Function: shared.FunctionDefinitionParam{
				Name:        def.Name,
				Description: param.NewOpt(def.Description),
				Parameters:  shared.FunctionParameters(def.Parameters),
			},
		})
	}
	return out
}

func assistantParam(res *llm.Result) openai.ChatCompletionMessageParamUnion {
	msg := openai.AssistantMessage(res.Content)
	if msg.OfAssistant == nil {
		return msg
	}
	msg.OfAssistant.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
		OfString: param.NewOpt(res.Content),
	}
	for _, call := range res.ToolCalls {
		msg.OfAssistant.ToolCalls = append(msg.OfAssistant.ToolCalls, openai.ChatCompletionMessageToolCallParam{
			ID:   call.ID,
			Type: "function",
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}
	return msg
}

func (a *Agent) buildSystemPrompt() string {
	base := strings.TrimSpace(a.opts.System)
	if base == "" {
		base = defaultSystemPrompt
	}
	if a.opts.Schema == nil {
		return base
	}
	schemaJSON, err := json.MarshalIndent(a.opts.Schema.Raw(), "", "  ")
	if err != nil {
		schemaJSON = []byte("{}")
	}
	return base + "\n\n" +
		"## Structured output (mandatory)\n" +
		"Reply with exactly one json value that validates against the JSON Schema below. " +
		"Do not wrap it in Markdown code fences. Do not add explanations before or after the json.\n\n" +
		"JSON Schema:\n" + string(schemaJSON)
}

func repairPrompt(validationErrs []string) string {
	var b strings.Builder
	b.WriteString("Your previous reply did not validate against the required JSON Schema.\n\n")
	b.WriteString("Validation errors:\n")
	for _, e := range validationErrs {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	b.WriteString("\nReturn ONLY a corrected json value that satisfies the schema. " +
		"No Markdown fences, no comments, no explanation.")
	return b.String()
}
