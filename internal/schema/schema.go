// Package schema implements the subset of JSON Schema that this CLI needs:
// enough to validate structured model output and feed precise errors back to
// the model for a repair round. It intentionally does not try to be a complete
// JSON Schema implementation.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Schema is a parsed JSON Schema node.
type Schema struct {
	Types                []string
	Properties           map[string]*Schema
	Required             []string
	AdditionalProperties *bool
	AdditionalSchema     *Schema
	Items                *Schema
	Enum                 []any
	Const                any
	Minimum              *float64
	Maximum              *float64
	MinLength            *int
	MaxLength            *int
	MinItems             *int
	MaxItems             *int
	Pattern              *regexp.Regexp
	AnyOf                []*Schema
	OneOf                []*Schema
	AllOf                []*Schema
	Description          string

	raw map[string]any
}

// Raw returns the original schema object, suitable for response_format.
func (s *Schema) Raw() map[string]any { return s.raw }

// Parse parses and compiles a JSON Schema document.
func Parse(data []byte) (*Schema, error) {
	var raw any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema root must be a JSON object")
	}
	s, err := parseNode(obj)
	if err != nil {
		return nil, err
	}
	s.raw = obj
	return s, nil
}

// ParseFile reads and parses a schema file.
func ParseFile(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}
	s, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func parseNode(obj map[string]any) (*Schema, error) {
	s := &Schema{}
	if ref, ok := obj["$ref"]; ok {
		return nil, fmt.Errorf("$ref is not supported (%v); inline the definition", ref)
	}
	if d, ok := obj["description"].(string); ok {
		s.Description = d
	}
	switch tv := obj["type"].(type) {
	case string:
		s.Types = []string{tv}
	case []any:
		for _, item := range tv {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("schema type array must contain strings")
			}
			s.Types = append(s.Types, str)
		}
	case nil:
	default:
		return nil, fmt.Errorf("schema type must be a string or array of strings")
	}
	for _, typ := range s.Types {
		switch typ {
		case "object", "array", "string", "number", "integer", "boolean", "null":
		default:
			return nil, fmt.Errorf("unsupported schema type %q", typ)
		}
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*Schema, len(props))
		for name, pv := range props {
			child, ok := pv.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("property %q must be an object schema", name)
			}
			parsed, err := parseNode(child)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			s.Properties[name] = parsed
		}
	}
	if req, ok := obj["required"].([]any); ok {
		for _, rv := range req {
			name, ok := rv.(string)
			if !ok {
				return nil, fmt.Errorf("required must contain strings")
			}
			s.Required = append(s.Required, name)
		}
	}
	if ap, ok := obj["additionalProperties"]; ok {
		switch v := ap.(type) {
		case bool:
			s.AdditionalProperties = &v
		case map[string]any:
			child, err := parseNode(v)
			if err != nil {
				return nil, fmt.Errorf("additionalProperties: %w", err)
			}
			s.AdditionalSchema = child
		default:
			return nil, fmt.Errorf("additionalProperties must be a boolean or a schema")
		}
	}
	if items, ok := obj["items"].(map[string]any); ok {
		child, err := parseNode(items)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		s.Items = child
	}
	if enum, ok := obj["enum"].([]any); ok {
		s.Enum = enum
	}
	if c, ok := obj["const"]; ok {
		s.Const = c
	}
	for key, dst := range map[string]**float64{"minimum": &s.Minimum, "maximum": &s.Maximum} {
		if v, ok := obj[key]; ok {
			f, ok := toFloat(v)
			if !ok {
				return nil, fmt.Errorf("%s must be a number", key)
			}
			*dst = &f
		}
	}
	for key, dst := range map[string]**int{
		"minLength": &s.MinLength, "maxLength": &s.MaxLength,
		"minItems": &s.MinItems, "maxItems": &s.MaxItems,
	} {
		if v, ok := obj[key]; ok {
			n, ok := toInt(v)
			if !ok || n < 0 {
				return nil, fmt.Errorf("%s must be a non-negative integer", key)
			}
			*dst = &n
		}
	}
	if p, ok := obj["pattern"].(string); ok {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern: %w", err)
		}
		s.Pattern = re
	}
	for key, dst := range map[string]*[]*Schema{"anyOf": &s.AnyOf, "oneOf": &s.OneOf, "allOf": &s.AllOf} {
		list, ok := obj[key].([]any)
		if !ok {
			continue
		}
		for i, item := range list {
			child, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be an object schema", key, i)
			}
			parsed, err := parseNode(child)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", key, i, err)
			}
			*dst = append(*dst, parsed)
		}
	}
	return s, nil
}

// ValidationErrors is the list of paths that failed validation.
type ValidationErrors struct {
	Errors []string
}

func (e *ValidationErrors) Error() string {
	if len(e.Errors) == 1 {
		return e.Errors[0]
	}
	return fmt.Sprintf("%d validation errors: %s", len(e.Errors), strings.Join(e.Errors, "; "))
}

// ValidateJSON parses data (strict JSON, no trailing prose) and validates it.
func (s *Schema) ValidateJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if errs := s.Validate(v); len(errs) > 0 {
		return v, &ValidationErrors{Errors: errs}
	}
	return v, nil
}

// Validate returns human-readable error strings prefixed with a JSON path.
func (s *Schema) Validate(v any) []string {
	var errs []string
	s.validate(v, "$", &errs)
	return errs
}

func (s *Schema) validate(v any, path string, errs *[]string) {
	add := func(format string, args ...any) {
		*errs = append(*errs, path+": "+fmt.Sprintf(format, args...))
	}
	for _, sub := range s.AllOf {
		sub.validate(v, path, errs)
	}
	if len(s.AnyOf) > 0 {
		matched := false
		for _, sub := range s.AnyOf {
			var subErrs []string
			sub.validate(v, path, &subErrs)
			if len(subErrs) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			add("does not match anyOf")
		}
	}
	if len(s.OneOf) > 0 {
		matched := 0
		for _, sub := range s.OneOf {
			var subErrs []string
			sub.validate(v, path, &subErrs)
			if len(subErrs) == 0 {
				matched++
			}
		}
		if matched != 1 {
			add("must match exactly one oneOf branch (matched %d)", matched)
		}
	}
	if s.Const != nil && !jsonEqual(v, s.Const) {
		add("must equal %s", compactJSON(s.Const))
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if jsonEqual(v, e) {
				found = true
				break
			}
		}
		if !found {
			add("must be one of %s", compactJSON(s.Enum))
		}
	}
	if len(s.Types) > 0 && !s.matchesType(v) {
		add("expected %s, got %s", strings.Join(s.Types, " or "), typeName(v))
		return // further checks would be misleading for the wrong type
	}

	if obj, ok := v.(map[string]any); ok {
		for _, name := range s.Required {
			if _, present := obj[name]; !present {
				*errs = append(*errs, fmt.Sprintf("%s.%s: required field is missing", path, name))
			}
		}
		for name, val := range obj {
			if child, ok := s.Properties[name]; ok {
				child.validate(val, path+"."+name, errs)
				continue
			}
			if s.AdditionalProperties != nil && !*s.AdditionalProperties {
				*errs = append(*errs, fmt.Sprintf("%s.%s: additional property is not allowed", path, name))
				continue
			}
			if s.AdditionalSchema != nil {
				s.AdditionalSchema.validate(val, path+"."+name, errs)
			}
		}
	}

	if arr, ok := v.([]any); ok {
		if s.MinItems != nil && len(arr) < *s.MinItems {
			add("expected at least %d items, got %d", *s.MinItems, len(arr))
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			add("expected at most %d items, got %d", *s.MaxItems, len(arr))
		}
		if s.Items != nil {
			for i, item := range arr {
				s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	}

	if str, ok := v.(string); ok {
		n := utf8.RuneCountInString(str)
		if s.MinLength != nil && n < *s.MinLength {
			add("expected at least %d characters, got %d", *s.MinLength, n)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			add("expected at most %d characters, got %d", *s.MaxLength, n)
		}
		if s.Pattern != nil && !s.Pattern.MatchString(str) {
			add("does not match pattern %q", s.Pattern.String())
		}
	}

	if f, ok := toFloat(v); ok {
		if s.Minimum != nil && f < *s.Minimum {
			add("must be >= %v", *s.Minimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			add("must be <= %v", *s.Maximum)
		}
	}
}

func (s *Schema) matchesType(v any) bool {
	for _, typ := range s.Types {
		switch typ {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		case "number":
			if _, ok := toFloat(v); ok {
				return true
			}
		case "integer":
			if f, ok := toFloat(v); ok && math.Trunc(f) == f {
				return true
			}
		}
	}
	return false
}

func typeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if f, err := x.Float64(); err == nil && math.Trunc(f) == f {
			return "integer"
		}
		return "number"
	case float64, float32, int, int64, int32:
		return "number"
	default:
		return reflect.TypeOf(v).String()
	}
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	default:
		return 0, false
	}
}

func toInt(v any) (int, bool) {
	f, ok := toFloat(v)
	if !ok || math.Trunc(f) != f || f > math.MaxInt32 || f < math.MinInt32 {
		return 0, false
	}
	return int(f), true
}

func jsonEqual(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}
