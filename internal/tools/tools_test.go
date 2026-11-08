package tools

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEval(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1+2*3", 7},
		{"(1+2)*3", 9},
		{"-2^2", -4},
		{"2^3^2", 512},
		{"2^-3", 0.125},
		{"10/4", 2.5},
		{"1e3/2", 500},
		{"sqrt(16)+max(1,2,3)", 7},
		{"pi", math.Pi},
		{"round(2.6) + floor(2.6) + ceil(2.1)", 8},
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr)
		if err != nil {
			t.Errorf("Eval(%q) error: %v", tc.expr, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	for _, expr := range []string{"", "1/0", "2+*3", "sqrt(", "sqrt(-1)", "foo(1)", "1..2", "(1+2"} {
		if _, err := Eval(expr); err == nil {
			t.Errorf("Eval(%q) should fail", expr)
		}
	}
}

func TestRegistryExecute(t *testing.T) {
	reg := NewRegistry(1000)
	if err := reg.Add(CalculatorTool()); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "calculator", json.RawMessage(`{"expression":"6*7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"result":42`) {
		t.Fatalf("out = %q", out)
	}
	if _, err := reg.Execute(context.Background(), "nope", nil); err == nil {
		t.Fatal("unknown tool should fail")
	}
	if _, err := reg.Execute(context.Background(), "calculator", json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid JSON should fail")
	}
}

func TestRegistryTruncatesOutput(t *testing.T) {
	reg := NewRegistry(5)
	err := reg.Add(&Tool{
		Name:       "echo",
		Parameters: ObjectSchema(nil),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "abcdefghij", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "abcde") || !strings.Contains(out, "truncated") {
		t.Fatalf("out = %q", out)
	}
}

func TestReadFileStaysInsideRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi there"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := ReadFileTool(dir)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi there" {
		t.Errorf("out = %q", out)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"../outside.txt"}`)); err == nil {
		t.Fatal("path escape should be rejected")
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"path":"missing.txt"}`)); err == nil {
		t.Fatal("missing file should fail")
	}
}
