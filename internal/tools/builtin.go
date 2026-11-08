package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Builtins returns the default offline tool set. root bounds read_file; it is
// resolved to an absolute path at construction time.
func Builtins(root string) []*Tool {
	return []*Tool{
		TimeTool(),
		CalculatorTool(),
		ReadFileTool(root),
	}
}

// TimeTool returns the current time in an IANA timezone.
func TimeTool() *Tool {
	return &Tool{
		Name:        "get_current_time",
		Description: "Get the current date and time. Use it whenever the answer depends on \"now\" instead of guessing.",
		Parameters: ObjectSchema(map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name such as Asia/Shanghai or America/New_York. Defaults to the machine's local zone.",
			},
		}),
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Timezone string `json:"timezone"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			loc := time.Local
			name := time.Local.String()
			if tz := strings.TrimSpace(args.Timezone); tz != "" && !strings.EqualFold(tz, "local") {
				l, err := time.LoadLocation(tz)
				if err != nil {
					return "", fmt.Errorf("unknown timezone %q (use an IANA name like Asia/Shanghai)", tz)
				}
				loc = l
				name = tz
			}
			now := time.Now().In(loc)
			out, _ := json.Marshal(map[string]any{
				"time":     now.Format(time.RFC3339),
				"timezone": name,
				"weekday":  now.Weekday().String(),
				"unix":     now.Unix(),
			})
			return string(out), nil
		},
	}
}

// ReadFileTool reads a UTF-8 text file under root. Paths that escape the root
// (including via symlinks) are rejected, making it safe to hand to a model.
func ReadFileTool(root string) *Tool {
	root = resolveRoot(root)
	return &Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file inside the working directory. Use a path relative to the working directory. Large files are truncated.",
		Parameters: ObjectSchema(map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path relative to the working directory, e.g. \"README.md\" or \"internal/llm/client.go\".",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     65536,
				"description": "Optional byte limit (default 32768, max 65536).",
			},
		}, "path"),
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Path     string `json:"path"`
				MaxBytes int    `json:"max_bytes"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Path) == "" {
				return "", fmt.Errorf("path is empty")
			}
			limit := args.MaxBytes
			if limit <= 0 {
				limit = 32768
			}
			if limit > 65536 {
				limit = 65536
			}
			target, err := secureJoin(root, args.Path)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(target)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("file not found: %s", args.Path)
				}
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory, not a file", args.Path)
			}
			if info.Size() == 0 {
				return "", nil
			}
			f, err := os.Open(target)
			if err != nil {
				return "", err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
			if err != nil {
				return "", err
			}
			if idx := indexByte(data, 0); idx >= 0 {
				return "", fmt.Errorf("%s looks like a binary file", args.Path)
			}
			truncated := len(data) > limit
			if truncated {
				data = data[:limit]
			}
			out := string(data)
			if truncated {
				out += fmt.Sprintf("\n\n...[truncated: showing first %d bytes of %d]", limit, info.Size())
			}
			return out, nil
		},
	}
}

func resolveRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func secureJoin(root, name string) (string, error) {
	target := name
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, name)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", name)
		}
		return "", err
	}
	rel, err := filepath.Rel(root, realTarget)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes the working directory", name)
	}
	return realTarget, nil
}

func indexByte(b []byte, want byte) int {
	for i, c := range b {
		if c == want {
			return i
		}
	}
	return -1
}
