package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sseHandler(t *testing.T, content string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		write := func(v any) {
			b, err := json.Marshal(v)
			if err != nil {
				t.Errorf("marshal: %v", err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		write(map[string]any{
			"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "mock",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		})
		write(map[string]any{
			"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "mock",
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	})
}

func TestRunStreamsAnswerAndPrintsUsage(t *testing.T) {
	srv := httptest.NewServer(sseHandler(t, "hello from the model"))
	defer srv.Close()

	var out, diag bytes.Buffer
	code := run([]string{
		"--base-url", srv.URL + "/v1",
		"--api-key", "test",
		"--no-tools",
		"--color", "never",
		"hello",
	}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, diag.String())
	}
	if strings.TrimSpace(out.String()) != "hello from the model" {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(diag.String(), "in 100") || !strings.Contains(diag.String(), "out 5") {
		t.Errorf("stderr missing usage: %s", diag.String())
	}
}

func TestRunStructuredOutput(t *testing.T) {
	srv := httptest.NewServer(sseHandler(t, "```json\n{\"name\":\"Ada\",\"age\":36}\n```"))
	defer srv.Close()

	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "person.json")
	if err := os.WriteFile(schemaPath, []byte(`{
		"type":"object",
		"properties":{"name":{"type":"string"},"age":{"type":"integer"}},
		"required":["name","age"],
		"additionalProperties":false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, diag bytes.Buffer
	code := run([]string{
		"--base-url", srv.URL + "/v1",
		"--api-key", "test",
		"--no-tools",
		"--color", "never",
		"--schema", schemaPath,
		"extract",
	}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, diag.String())
	}
	if !strings.Contains(out.String(), `"name": "Ada"`) || !strings.Contains(out.String(), `"age": 36`) {
		t.Errorf("stdout = %q", out.String())
	}
	if strings.Contains(out.String(), "```") {
		t.Errorf("stdout contains markdown fence: %q", out.String())
	}
}

func TestRunOfflineReturnsExitOneAndHint(t *testing.T) {
	var out, diag bytes.Buffer
	code := run([]string{
		"--base-url", "http://127.0.0.1:1",
		"--api-key", "test",
		"--max-retries", "0",
		"--timeout", "2s",
		"--no-tools",
		"--color", "never",
		"hi",
	}, strings.NewReader(""), &out, &diag)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, diag.String())
	}
	if !strings.Contains(diag.String(), "网络连接失败") || !strings.Contains(diag.String(), "无法连接") {
		t.Errorf("stderr = %s", diag.String())
	}
}

func TestToolsCommand(t *testing.T) {
	var out, diag bytes.Buffer
	code := run([]string{"tools"}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"get_current_time", "calculator", "read_file"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("tools output missing %s: %s", want, out.String())
		}
	}
}

// 回归：问题写在前面时，后面的 flag 仍须生效，不能被拼进问题文本。
func TestFlagsAfterQuestion(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- body
		sseHandler(t, "flag order ok").ServeHTTP(w, r)
	}))
	defer srv.Close()

	var out, diag bytes.Buffer
	code := run([]string{
		"hello world", // 问题在前（带引号）
		"--base-url", srv.URL + "/v1",
		"--api-key", "test",
		"--no-tools",
		"--color", "never",
	}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, diag.String())
	}
	if got := strings.TrimSpace(out.String()); got != "flag order ok" {
		t.Errorf("stdout = %q", got)
	}
	req := string(<-bodyCh)
	if !strings.Contains(req, "hello world") {
		t.Errorf("question was not preserved: %s", req)
	}
	for _, leaked := range []string{"--no-tools", "--base-url", "--color"} {
		if strings.Contains(req, leaked) {
			t.Errorf("flag %s leaked into the question: %s", leaked, req)
		}
	}
}

// 同一个问题不加引号、flag 写在后面也要成立。
func TestUnquotedQuestionBeforeFlags(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- body
		sseHandler(t, "ok").ServeHTTP(w, r)
	}))
	defer srv.Close()

	var out, diag bytes.Buffer
	code := run([]string{
		"现在", "几点", "了",
		"--base-url", srv.URL + "/v1",
		"--api-key", "test",
		"--no-tools",
		"--color", "never",
	}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, diag.String())
	}
	if !strings.Contains(string(<-bodyCh), "现在 几点 了") {
		t.Errorf("question was not preserved")
	}
}

func TestSplitQuestion(t *testing.T) {
	cfg := defaultConfig()
	var key string
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	registerFlags(fs, &cfg, &key)

	cases := []struct {
		name  string
		args  []string
		wantQ string
		wantF string
	}{
		{"flags first", []string{"--no-tools", "hi"}, "hi", "--no-tools"},
		{"flags last", []string{"hi", "--no-tools"}, "hi", "--no-tools"},
		{"value flag", []string{"hi", "--model", "gpt-4o"}, "hi", "--model|gpt-4o"},
		{"negative value", []string{"-temperature", "-1", "hi"}, "hi", "-temperature|-1"},
		{"equals form", []string{"--base-url=http://x", "hi"}, "hi", "--base-url=http://x"},
		{"double dash", []string{"--", "--no-tools"}, "--no-tools", ""},
		{"multi word", []string{"现在", "几点", "--no-tools"}, "现在 几点", "--no-tools"},
		{"unknown flag", []string{"hi", "--nope"}, "hi", "--nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, f := splitQuestion(fs, tc.args)
			if q != tc.wantQ {
				t.Errorf("question = %q, want %q", q, tc.wantQ)
			}
			if got := strings.Join(f, "|"); got != tc.wantF {
				t.Errorf("flags = %q, want %q", got, tc.wantF)
			}
		})
	}
}
