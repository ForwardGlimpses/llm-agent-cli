package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentcli/internal/agent"
	"agentcli/internal/llm"
	"agentcli/internal/schema"
	"agentcli/internal/tools"
)

const (
	testModel = "mock-model"
	testKey   = "test-key"
)

type wireRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Messages []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	ResponseFormat json.RawMessage `json:"response_format"`
}

func (m wireRequest) lastMessage() (string, string) {
	if len(m.Messages) == 0 {
		return "", ""
	}
	last := m.Messages[len(m.Messages)-1]
	var s string
	_ = json.Unmarshal(last.Content, &s)
	return last.Role, s
}

type recorder struct {
	mu   sync.Mutex
	reqs []wireRequest
	raw  []string
}

func (r *recorder) add(req wireRequest, raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	r.raw = append(r.raw, raw)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *recorder) at(i int) wireRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[i]
}

func decodeRequest(t *testing.T, r *http.Request) wireRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read body: %v", err)
		return wireRequest{}
	}
	var req wireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Errorf("decode request: %v; body=%s", err, body)
	}
	return req
}

func sseHeaders(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return w.(http.Flusher)
}

func writeEvent(t *testing.T, w io.Writer, fl http.Flusher, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Errorf("marshal event: %v", err)
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}

func writeDone(w io.Writer, fl http.Flusher) {
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

func chunk(delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   testModel,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
}

func usageChunk(prompt, completion int) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   testModel,
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"total_tokens":      prompt + completion,
		},
	}
}

func toolCallDelta(index int, id, name, args string) map[string]any {
	fn := map[string]any{"arguments": args}
	if name != "" {
		fn["name"] = name
	}
	m := map[string]any{"index": index, "function": fn}
	if id != "" {
		m["id"] = id
		m["type"] = "function"
	}
	return m
}

func newClient(url string, retries int) *llm.Client {
	return llm.New(llm.Config{
		BaseURL:        url,
		APIKey:         testKey,
		Model:          testModel,
		MaxRetries:     retries,
		BackoffBase:    time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		RequestTimeout: 5 * time.Second,
	})
}

func testRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry(4000)
	err := reg.Add(&tools.Tool{
		Name:        "echo",
		Description: "echo text",
		Parameters: tools.ObjectSchema(map[string]any{
			"text": map[string]any{"type": "string"},
		}, "text"),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			return "echo:" + in.Text, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestRunExecutesToolAndFeedsResultBack(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		rec.add(req, "")
		fl := sseHeaders(w)
		if rec.count() == 1 {
			writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": ""}, nil))
			writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
				toolCallDelta(0, "call_1", "echo", `{"text":`),
			}}, nil))
			writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
				toolCallDelta(0, "", "", `"hello"}`),
			}}, "tool_calls"))
			writeEvent(t, w, fl, usageChunk(100, 10))
		} else {
			writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "final "}, nil))
			writeEvent(t, w, fl, chunk(map[string]any{"content": "answer"}, "stop"))
			writeEvent(t, w, fl, usageChunk(120, 5))
		}
		writeDone(w, fl)
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{})
	res, err := a.Run(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Content != "final answer" {
		t.Errorf("content = %q", res.Content)
	}
	if res.ToolCalls != 1 || res.Requests != 2 {
		t.Errorf("toolCalls=%d requests=%d", res.ToolCalls, res.Requests)
	}
	if res.Stats.Prompt != 220 || res.Stats.Completion != 15 || res.Stats.Total != 235 {
		t.Errorf("stats = %+v", res.Stats)
	}
	if rec.count() != 2 {
		t.Fatalf("requests = %d", rec.count())
	}

	first := rec.at(0)
	if !first.Stream || !first.StreamOptions.IncludeUsage {
		t.Errorf("expected stream+include_usage, got %+v", first.StreamOptions)
	}
	if len(first.Tools) != 1 || first.Tools[0].Function.Name != "echo" {
		t.Errorf("tools = %+v", first.Tools)
	}

	second := rec.at(1)
	if len(second.Messages) != 4 {
		t.Fatalf("second request messages = %d: %+v", len(second.Messages), second.Messages)
	}
	if second.Messages[2].Role != "assistant" || len(second.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", second.Messages[2])
	}
	call := second.Messages[2].ToolCalls[0]
	if call.ID != "call_1" || call.Function.Arguments != `{"text":"hello"}` {
		t.Errorf("tool call = %+v", call)
	}
	if second.Messages[3].Role != "tool" || second.Messages[3].ToolCallID != "call_1" {
		t.Errorf("tool message = %+v", second.Messages[3])
	}
	var toolContent string
	_ = json.Unmarshal(second.Messages[3].Content, &toolContent)
	if toolContent != "echo:hello" {
		t.Errorf("tool content = %q", toolContent)
	}
}

func TestParallelToolCallsAreExecutedInIndexOrder(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(decodeRequest(t, r), "")
		fl := sseHeaders(w)
		if rec.count() == 1 {
			writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": ""}, nil))
			writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
				toolCallDelta(0, "call_a", "echo", `{"text":`),
				toolCallDelta(1, "call_b", "echo", `{"text":`),
			}}, nil))
			writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
				toolCallDelta(1, "", "", `"two"}`),
				toolCallDelta(0, "", "", `"one"}`),
			}}, "tool_calls"))
		} else {
			writeEvent(t, w, fl, chunk(map[string]any{"content": "both done"}, "stop"))
		}
		writeDone(w, fl)
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{})
	res, err := a.Run(context.Background(), "run both")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ToolCalls != 2 || res.Content != "both done" {
		t.Errorf("toolCalls=%d content=%q", res.ToolCalls, res.Content)
	}
	if rec.count() != 2 {
		t.Fatalf("requests = %d", rec.count())
	}
	second := rec.at(1)
	if len(second.Messages) != 5 {
		t.Fatalf("messages = %d: %+v", len(second.Messages), second.Messages)
	}
	asst := second.Messages[2]
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("assistant tool_calls = %d", len(asst.ToolCalls))
	}
	if asst.ToolCalls[0].ID != "call_a" || asst.ToolCalls[0].Function.Arguments != `{"text":"one"}` {
		t.Errorf("call 0 = %+v", asst.ToolCalls[0])
	}
	if asst.ToolCalls[1].ID != "call_b" || asst.ToolCalls[1].Function.Arguments != `{"text":"two"}` {
		t.Errorf("call 1 = %+v", asst.ToolCalls[1])
	}
	for i, wantID := range []string{"call_a", "call_b"} {
		msg := second.Messages[3+i]
		if msg.Role != "tool" || msg.ToolCallID != wantID {
			t.Errorf("tool message %d = %+v", i, msg)
		}
	}
}

func TestRunRetriesRateLimitBeforeOutput(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			return
		}
		fl := sseHeaders(w)
		writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "ok"}, "stop"))
		writeDone(w, fl)
	}))
	defer srv.Close()

	var retries []llm.RetryInfo
	a := agent.New(newClient(srv.URL+"/v1", 3), testRegistry(t), agent.Options{
		Hooks: agent.Hooks{OnRetry: func(info llm.RetryInfo) { retries = append(retries, info) }},
	})
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Content != "ok" || res.Requests != 1 {
		t.Errorf("content=%q requests=%d", res.Content, res.Requests)
	}
	if len(retries) != 2 {
		t.Fatalf("retries = %d (%+v)", len(retries), retries)
	}
	if retries[0].Status != 429 || retries[1].Attempt != 2 {
		t.Errorf("retry info = %+v", retries)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Errorf("attempts = %d", attempts)
	}
}

func TestNoRetryAfterPartialOutput(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		fl := sseHeaders(w)
		writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "par"}, nil))
		fmt.Fprint(w, "data: {oops not json\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 3), testRegistry(t), agent.Options{})
	res, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected an error")
	}
	var le *llm.Error
	if !errors.As(err, &le) {
		t.Fatalf("error type = %T", err)
	}
	if !le.Partial {
		t.Error("Partial should be true")
	}
	if res.Content != "par" {
		t.Errorf("partial content = %q", res.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("must not retry after output; attempts = %d", attempts)
	}
}

func TestOfflineErrorHasActionableHint(t *testing.T) {
	// Port 1 is reserved and closed, so dial fails immediately.
	client := llm.New(llm.Config{
		BaseURL:        "http://127.0.0.1:1",
		APIKey:         "x",
		Model:          testModel,
		MaxRetries:     0,
		RequestTimeout: time.Second,
		BackoffBase:    time.Millisecond,
	})
	a := agent.New(client, testRegistry(t), agent.Options{})
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error")
	}
	var le *llm.Error
	if !errors.As(err, &le) {
		t.Fatalf("error type = %T: %v", err, err)
	}
	if le.Kind != llm.KindNetwork {
		t.Errorf("kind = %s (%v)", le.Kind, err)
	}
	if !strings.Contains(le.Hint(), "无法连接") {
		t.Errorf("hint = %q", le.Hint())
	}
	if le.Attempts != 1 {
		t.Errorf("attempts = %d", le.Attempts)
	}
}

func TestContextCancellationDuringStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := sseHeaders(w)
		writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "thinking"}, nil))
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{})
		_, err := a.Run(ctx, "hi")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRequestTimeoutIsNotRetried(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		fl := sseHeaders(w)
		fl.Flush()
		// 只发响应头、不发任何 delta：Started 保持 false，这样唯一能阻止重试的
		// 就是“请求超时不重试”这条规则本身。之前先发一个 content chunk 会让
		// “已输出不重试”先短路，测试名不副实。
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	client := llm.New(llm.Config{
		BaseURL:        srv.URL + "/v1",
		APIKey:         testKey,
		Model:          testModel,
		MaxRetries:     2,
		BackoffBase:    time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		RequestTimeout: 150 * time.Millisecond,
	})
	a := agent.New(client, testRegistry(t), agent.Options{})
	start := time.Now()
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected timeout")
	}
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.KindTimeout {
		t.Fatalf("error = %v (%T)", err, err)
	}
	if le.Attempts != 1 {
		t.Errorf("attempts = %d, timeout must not be retried", le.Attempts)
	}
	if le.Partial {
		t.Error("Partial must be false: no delta was emitted before the deadline")
	}
	if time.Since(start) > time.Second {
		t.Errorf("timeout did not fire promptly: %v", time.Since(start))
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("attempts = %d", attempts)
	}
}

func TestStructuredOutputRepair(t *testing.T) {
	personSchema, err := schema.Parse([]byte(`{
		"type":"object",
		"properties":{"name":{"type":"string"},"age":{"type":"integer","minimum":0}},
		"required":["name","age"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		rec.add(req, "")
		fl := sseHeaders(w)
		if rec.count() == 1 {
			writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "```json\n{\"name\":\"Ada\",\"age\":\"old\"}\n```"}, "stop"))
		} else {
			writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": "{\"name\":\"Ada\",\"age\":36}"}, "stop"))
		}
		writeDone(w, fl)
	}))
	defer srv.Close()

	var repairs [][]string
	a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{
		Schema:     personSchema,
		MaxRepairs: 2,
		Hooks: agent.Hooks{OnRepair: func(errs []string, attempt int) {
			repairs = append(repairs, errs)
		}},
	})
	res, err := a.Run(context.Background(), "extract Ada")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(res.Structured) != `{"name":"Ada","age":36}` {
		t.Errorf("structured = %s", res.Structured)
	}
	if res.Repairs != 1 || len(repairs) != 1 {
		t.Fatalf("repairs = %d", res.Repairs)
	}
	if !strings.Contains(strings.Join(repairs[0], " "), "expected integer") {
		t.Errorf("repair errors = %v", repairs[0])
	}
	second := rec.at(1)
	role, content := second.lastMessage()
	if role != "user" || !strings.Contains(content, "did not validate") {
		t.Errorf("repair message = %s: %s", role, content)
	}
	// The schema must be embedded in the system prompt.
	var sys string
	_ = json.Unmarshal(second.Messages[0].Content, &sys)
	if !strings.Contains(sys, "JSON Schema") || !strings.Contains(sys, `"age"`) {
		t.Errorf("system prompt lacks schema: %s", sys)
	}
}

func TestStructuredOutputGivesUpAfterRepairs(t *testing.T) {
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		fl := sseHeaders(w)
		writeEvent(t, w, fl, chunk(map[string]any{"role": "assistant", "content": `{"n":"not a number"}`}, "stop"))
		writeDone(w, fl)
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{Schema: s, MaxRepairs: 1})
	_, err = a.Run(context.Background(), "x")
	var se *agent.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("error type = %T: %v", err, err)
	}
	if se.Attempts != 2 {
		t.Errorf("attempts = %d", se.Attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("requests = %d, want 2 (1 initial + 1 repair)", requests)
	}
}

func TestToolErrorIsReportedToModel(t *testing.T) {
	reg := tools.NewRegistry(4000)
	if err := reg.Add(&tools.Tool{
		Name:       "boom",
		Parameters: tools.ObjectSchema(nil),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "", errors.New("kaboom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(decodeRequest(t, r), "")
		fl := sseHeaders(w)
		if rec.count() == 1 {
			writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
				toolCallDelta(0, "call_x", "boom", "{}"),
			}}, "tool_calls"))
		} else {
			writeEvent(t, w, fl, chunk(map[string]any{"content": "recovered"}, "stop"))
		}
		writeDone(w, fl)
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 0), reg, agent.Options{})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Content != "recovered" {
		t.Errorf("content = %q", res.Content)
	}
	second := rec.at(1)
	if len(second.Messages) != 4 {
		t.Fatalf("messages = %d", len(second.Messages))
	}
	var toolContent string
	_ = json.Unmarshal(second.Messages[3].Content, &toolContent)
	if !strings.HasPrefix(toolContent, "ERROR:") || !strings.Contains(toolContent, "kaboom") {
		t.Errorf("tool content = %q", toolContent)
	}
}

func TestMaxTurns(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		fl := sseHeaders(w)
		writeEvent(t, w, fl, chunk(map[string]any{"tool_calls": []any{
			toolCallDelta(0, "call_x", "echo", `{"text":"again"}`),
		}}, "tool_calls"))
		writeDone(w, fl)
	}))
	defer srv.Close()

	a := agent.New(newClient(srv.URL+"/v1", 0), testRegistry(t), agent.Options{MaxTurns: 2})
	_, err := a.Run(context.Background(), "loop")
	var mt *agent.MaxTurnsError
	if !errors.As(err, &mt) {
		t.Fatalf("error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("requests = %d", requests)
	}
}
