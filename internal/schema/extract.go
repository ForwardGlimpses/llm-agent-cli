package schema

import (
	"encoding/json"
	"strings"
)

// ExtractJSON pulls a JSON value out of a model response.
//
// Models frequently wrap output in Markdown fences or add a sentence such as
// "Here is the JSON:". The parser is strict, but extraction is deliberately
// forgiving; schema validation still happens afterwards.
func ExtractJSON(s string) ([]byte, error) {
	t := strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
	if t == "" {
		return nil, &ExtractError{Reason: "empty response"}
	}
	if json.Valid([]byte(t)) {
		return []byte(t), nil
	}

	// Markdown code fence: ```json ... ``` (or ``` ... ```).
	if strings.HasPrefix(t, "```") {
		if inner, ok := unfence(t); ok {
			if json.Valid([]byte(inner)) {
				return []byte(inner), nil
			}
			if region, ok := findBalanced(inner); ok {
				return []byte(region), nil
			}
		}
	}

	if region, ok := findBalanced(t); ok {
		return []byte(region), nil
	}

	// A bare JSON string/number/bool is legal JSON but not an object/array.
	trimmed := strings.TrimSpace(t)
	if json.Valid([]byte(trimmed)) {
		return []byte(trimmed), nil
	}
	return nil, &ExtractError{Reason: "no JSON value found in response"}
}

// ExtractError means no parseable JSON was present at all.
type ExtractError struct {
	Reason string
}

func (e *ExtractError) Error() string { return e.Reason }

func unfence(s string) (string, bool) {
	firstNL := strings.IndexByte(s, '\n')
	if firstNL < 0 {
		return "", false
	}
	body := s[firstNL+1:]
	body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	return strings.TrimSpace(body), true
}

// findBalanced scans for the first '{' or '[' and returns the shortest balanced
// JSON prefix starting there. Strings and escapes are tracked so braces inside
// strings do not confuse the scan.
func findBalanced(s string) (string, bool) {
	for start := 0; start < len(s); start++ {
		c := s[start]
		if c != '{' && c != '[' {
			continue
		}
		depth := 0
		inString := false
		escaped := false
		for i := start; i < len(s); i++ {
			ch := s[i]
			if inString {
				if escaped {
					escaped = false
					continue
				}
				switch ch {
				case '\\':
					escaped = true
				case '"':
					inString = false
				}
				continue
			}
			switch ch {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					candidate := s[start : i+1]
					if json.Valid([]byte(candidate)) {
						return candidate, true
					}
				}
			}
		}
	}
	return "", false
}
