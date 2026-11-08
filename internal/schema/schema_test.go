package schema

import (
	"strings"
	"testing"
)

const personSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 2},
    "age": {"type": "integer", "minimum": 0, "maximum": 150},
    "tags": {"type": "array", "items": {"type": "string"}, "minItems": 1},
    "role": {"type": "string", "enum": ["admin", "user"]}
  },
  "required": ["name", "age"],
  "additionalProperties": false
}`

func TestValidateValid(t *testing.T) {
	s, err := Parse([]byte(personSchema))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ValidateJSON([]byte(`{"name":"Ada","age":36,"tags":["math"],"role":"admin"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("value is nil")
	}
}

func TestValidateReportsUsefulPaths(t *testing.T) {
	s, err := Parse([]byte(personSchema))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		input string
		want  string
	}{
		{`{"age": 36}`, "$.name: required field is missing"},
		{`{"name":"Ada","age":200}`, "must be <= 150"},
		{`{"name":"A","age":1}`, "at least 2 characters"},
		{`{"name":"Ada","age":1,"role":"root"}`, "must be one of"},
		{`{"name":"Ada","age":1,"extra":true}`, "additional property is not allowed"},
		{`{"name":"Ada","age":1.5}`, "expected integer, got number"},
		{`{"name":"Ada","age":1,"tags":[]}`, "at least 1 items"},
	}
	for _, tc := range cases {
		_, err := s.ValidateJSON([]byte(tc.input))
		if err == nil {
			t.Errorf("%s: expected validation error", tc.input)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not contain %q", tc.input, err, tc.want)
		}
	}
}

func TestValidateNestedPath(t *testing.T) {
	s, err := Parse([]byte(`{
		"type":"object",
		"properties":{"user":{"type":"object","properties":{"email":{"type":"string","pattern":"@"}},"required":["email"]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ValidateJSON([]byte(`{"user":{"email":"nope"}}`))
	if err == nil || !strings.Contains(err.Error(), "$.user.email") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateAnyOf(t *testing.T) {
	s, err := Parse([]byte(`{"anyOf":[{"type":"string"},{"type":"integer"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if errs := s.Validate(float64(3)); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if errs := s.Validate(true); len(errs) == 0 {
		t.Fatal("expected anyOf failure")
	}
}

func TestParseRejectsRef(t *testing.T) {
	if _, err := Parse([]byte(`{"$ref":"#/$defs/x"}`)); err == nil {
		t.Fatal("expected $ref error")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"prose", "Here you go:\n{\"a\": 1}\nHope it helps.", `{"a": 1}`},
		{"brace in string", `prefix {"a":"}"} suffix`, `{"a":"}"}`},
		{"array", "```\n[1,2,3]\n```", `[1,2,3]`},
	}
	for _, tc := range cases {
		got, err := ExtractJSON(tc.input)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := ExtractJSON("no json here"); err == nil {
		t.Fatal("expected extraction error")
	}
}

func TestExtractJSONDoesNotLeakTrailingProse(t *testing.T) {
	got, err := ExtractJSON(`{"a":{"b":1}} trailing words`)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":{"b":1}}` {
		t.Fatalf("got %q", got)
	}
}
