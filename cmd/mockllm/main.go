// Command mockllm is a tiny OpenAI-compatible streaming server for demos and
// offline development. It is not a model: it returns canned answers, but it
// speaks the same SSE protocol, including fragmented tool_calls.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (m chatMessage) text() string {
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(m.Content)
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Tools    []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	ResponseFormat json.RawMessage `json:"response_format"`
}

var calcExpr = regexp.MustCompile(`[0-9][0-9+\-*/^(). ]*`)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/chat/completions", handle)
	mux.HandleFunc("/chat/completions", handle)
	log.Printf("mockllm listening on http://%s", *addr)
	log.Printf("try: go run ./cmd/agent --base-url http://%s/v1 --api-key mock \"现在几点？\"", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("request model=%s messages=%d tools=%d stream=%v", req.Model, len(req.Messages), len(req.Tools), req.Stream)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)

	last := chatMessage{}
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1]
	}

	if sample, ok := structuredSample(req); ok {
		writeText(w, fl, req, string(sample), 20)
		return
	}

	if last.Role == "tool" {
		writeText(w, fl, req, toolAnswer(last.text()), 12)
		return
	}

	user := last.text()
	if strings.ContainsAny(user, "几点时间") || strings.Contains(strings.ToLower(user), "time") {
		if toolAvailable(req, "get_current_time") {
			writeToolCall(w, fl, req, "get_current_time", `{"timezone":"Asia/Shanghai"}`)
			return
		}
		now := time.Now().Format(time.RFC3339)
		writeText(w, fl, req, "现在是 "+now+"（mock 回答，未调用工具）。", 10)
		return
	}

	if strings.Contains(user, "计算") || strings.Contains(user, "算一下") {
		if toolAvailable(req, "calculator") {
			expr := "123*45+6"
			if m := calcExpr.FindString(user); strings.ContainsAny(m, "+-*/^") {
				expr = strings.TrimSpace(m)
			}
			args, _ := json.Marshal(map[string]string{"expression": expr})
			writeToolCall(w, fl, req, "calculator", string(args))
			return
		}
	}

	reply := fmt.Sprintf("我是本地 mock 模型（没有真实智能）。你刚才说：%q。\n\n"+
		"这个服务用于演示 SSE 流式输出、tool calling、重试与 token 统计。\n"+
		"把 --base-url 换成 https://api.deepseek.com 并设置 DEEPSEEK_API_KEY，就能得到真实模型的回答。", truncateRunes(user, 60))
	writeText(w, fl, req, reply, 14)
}

func toolAvailable(req chatRequest, name string) bool {
	for _, t := range req.Tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

func toolAnswer(result string) string {
	var t struct {
		Time     string `json:"time"`
		Timezone string `json:"timezone"`
	}
	if json.Unmarshal([]byte(result), &t) == nil && t.Time != "" {
		return fmt.Sprintf("现在是 %s（%s）。这是通过 get_current_time 工具得到的真实系统时间。", t.Time, t.Timezone)
	}
	var c struct {
		Expression string  `json:"expression"`
		Result     float64 `json:"result"`
	}
	if json.Unmarshal([]byte(result), &c) == nil && c.Expression != "" {
		return fmt.Sprintf("%s = %g。这是通过 calculator 工具计算的结果。", c.Expression, c.Result)
	}
	if strings.HasPrefix(result, "ERROR:") {
		return "工具执行失败：" + strings.TrimPrefix(result, "ERROR:")
	}
	return "工具返回：" + truncateRunes(result, 500)
}

func writeText(w io.Writer, fl http.Flusher, req chatRequest, text string, chunkRunes int) {
	writeEvent(w, fl, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model": modelOf(req), "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil,
		}},
	})
	parts := splitRunes(text, chunkRunes)
	for i, part := range parts {
		last := i == len(parts)-1
		var finish any
		if last {
			finish = "stop"
		}
		writeEvent(w, fl, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model": modelOf(req), "choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"content": part}, "finish_reason": finish,
			}},
		})
		time.Sleep(18 * time.Millisecond)
	}
	writeUsage(w, fl, req, promptTokens(req), approxTokens(text))
	writeDone(w, fl)
}

func writeToolCall(w io.Writer, fl http.Flusher, req chatRequest, name, args string) {
	writeEvent(w, fl, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model": modelOf(req), "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil,
		}},
	})
	writeEvent(w, fl, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model": modelOf(req), "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_mock_1", "type": "function",
				"function": map[string]any{"name": name, "arguments": ""},
			}}}, "finish_reason": nil,
		}},
	})
	// Split the argument JSON mid-string on purpose: this exercises the
	// index-based assembly in the client.
	for _, part := range splitRunes(args, (utf8.RuneCountInString(args)+2)/3) {
		writeEvent(w, fl, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model": modelOf(req), "choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "function": map[string]any{"arguments": part},
				}}}, "finish_reason": nil,
			}},
		})
		time.Sleep(30 * time.Millisecond)
	}
	writeEvent(w, fl, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model": modelOf(req), "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}},
	})
	writeUsage(w, fl, req, promptTokens(req), 20)
	writeDone(w, fl)
}

func writeUsage(w io.Writer, fl http.Flusher, req chatRequest, prompt, completion int) {
	if !req.StreamOptions.IncludeUsage {
		// Real servers only send usage when asked; the CLI always asks.
		return
	}
	writeEvent(w, fl, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model": modelOf(req), "choices": []any{}, "usage": map[string]any{
			"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion,
			"prompt_tokens_details": map[string]any{"cached_tokens": 0},
		},
	})
}

func writeEvent(w io.Writer, fl http.Flusher, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if fl != nil {
		fl.Flush()
	}
}

func writeDone(w io.Writer, fl http.Flusher) {
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func modelOf(req chatRequest) string {
	if req.Model == "" {
		return "mock-model"
	}
	return req.Model
}

func promptTokens(req chatRequest) int {
	total := 8
	for _, m := range req.Messages {
		total += approxTokens(m.text()) + 4
	}
	return total
}

func splitRunes(s string, n int) []string {
	if s == "" {
		return []string{""}
	}
	if n < 1 {
		n = 1
	}
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		take := n
		if take > len(runes) {
			take = len(runes)
		}
		out = append(out, string(runes[:take]))
		runes = runes[take:]
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func approxTokens(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 1
	}
	return n/2 + 1
}

// structuredSample derives a sample value from the JSON Schema embedded in the
// system prompt, so `agent -schema ...` works against the mock too.
func structuredSample(req chatRequest) ([]byte, bool) {
	var sys string
	for _, m := range req.Messages {
		if m.Role == "system" {
			sys += m.text() + "\n"
		}
	}
	const marker = "JSON Schema:"
	idx := strings.LastIndex(sys, marker)
	if idx < 0 {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(sys[idx+len(marker):])))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, false
	}
	b, err := json.MarshalIndent(sampleFromSchema(raw), "", "  ")
	if err != nil {
		return nil, false
	}
	return b, true
}

func sampleFromSchema(s map[string]any) any {
	if v, ok := s["const"]; ok {
		return v
	}
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		return e[0]
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := s[key].([]any); ok && len(list) > 0 {
			if m, ok := list[0].(map[string]any); ok {
				return sampleFromSchema(m)
			}
		}
	}
	typ, _ := s["type"].(string)
	switch typ {
	case "object":
		out := map[string]any{}
		if props, ok := s["properties"].(map[string]any); ok {
			for name, pv := range props {
				if pm, ok := pv.(map[string]any); ok {
					out[name] = sampleFromSchema(pm)
				}
			}
		}
		return out
	case "array":
		if items, ok := s["items"].(map[string]any); ok {
			return []any{sampleFromSchema(items)}
		}
		return []any{}
	case "integer", "number":
		v := 30.0
		if m, ok := s["minimum"].(float64); ok {
			v = m + 30
		}
		if m, ok := s["maximum"].(float64); ok && v > m {
			v = m
		}
		if typ == "integer" {
			return int(v)
		}
		return v
	case "boolean":
		return true
	case "null":
		return nil
	default:
		return "示例文本"
	}
}
