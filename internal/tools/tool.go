// Package tools contains a tiny, dependency-free tool registry used by the
// agent loop. Tools are plain Go functions with a JSON Schema for their
// arguments; the registry adapts them to the OpenAI tool format and guards
// execution (panic recovery, output truncation, JSON validation).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Handler executes a tool. args is the raw JSON object produced by the model.
type Handler func(ctx context.Context, args json.RawMessage) (string, error)

// Tool is one callable function exposed to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     Handler
}

// Registry holds the tools available for a session.
type Registry struct {
	tools     map[string]*Tool
	order     []string
	maxOutput int
}

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// NewRegistry creates an empty registry. maxOutput is the per-call character
// cap for tool results (0 means 8000); oversized results are truncated with a
// marker so a chatty tool cannot blow up the context window.
func NewRegistry(maxOutput int) *Registry {
	if maxOutput <= 0 {
		maxOutput = 8000
	}
	return &Registry{tools: make(map[string]*Tool), maxOutput: maxOutput}
}

// Add registers a tool. Duplicate names are rejected.
func (r *Registry) Add(t *Tool) error {
	if t == nil || t.Handler == nil {
		return fmt.Errorf("tools: nil tool or handler")
	}
	if !toolNameRE.MatchString(t.Name) {
		return fmt.Errorf("tools: invalid name %q (want [a-zA-Z0-9_-]{1,64})", t.Name)
	}
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tools: duplicate tool %q", t.Name)
	}
	if t.Parameters == nil {
		t.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// Names returns the registered tool names in insertion order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// All returns the registered tools in insertion order.
func (r *Registry) All() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Execute validates and runs a tool call. Errors are returned as normal errors
// (the agent turns them into a tool message so the model can recover).
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		available := r.Names()
		sort.Strings(available)
		return "", fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(available, ", "))
	}
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return "", fmt.Errorf("arguments for %s are not a JSON object: %v", name, err)
	}

	var (
		out string
		err error
	)
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("tool %s panicked: %v", name, rec)
			}
		}()
		out, err = t.Handler(ctx, json.RawMessage(trimmed))
	}()
	if err != nil {
		return "", err
	}
	return truncate(out, r.maxOutput), nil
}

func truncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + fmt.Sprintf("\n\n...[truncated: %d of %d characters shown]", max, len(runes))
}

// ObjectSchema is a small helper for declaring tool parameters.
func ObjectSchema(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
