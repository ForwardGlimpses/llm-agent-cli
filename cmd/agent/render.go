package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"agentcli/internal/agent"
	"agentcli/internal/llm"
	"agentcli/internal/usage"
)

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// renderer separates machine-readable output (stdout: streamed answer / JSON)
// from diagnostics (stderr: tool traces, retries, token accounting). This keeps
// `agent "..." | jq` usable.
type renderer struct {
	out  io.Writer
	diag io.Writer

	ui            bool // terminal: prefixes and colors
	color         bool
	showReasoning bool
	verbose       bool
	quiet         bool

	textStarted      bool
	reasoningOpen    bool
	reasoningStarted bool
}

func newRenderer(out, diag io.Writer, ui, color, showReasoning, verbose, quiet bool) *renderer {
	return &renderer{
		out:           out,
		diag:          diag,
		ui:            ui,
		color:         color,
		showReasoning: showReasoning,
		verbose:       verbose,
		quiet:         quiet,
	}
}

func (r *renderer) paint(code, s string) string {
	if !r.color || s == "" {
		return s
	}
	return code + s + ansiReset
}

func (r *renderer) dim(s string) string { return r.paint(ansiDim, s) }

// StartTurn resets per-turn rendering state.
func (r *renderer) StartTurn() {
	r.textStarted = false
	r.reasoningOpen = false
	r.reasoningStarted = false
}

// Text streams assistant text to stdout.
func (r *renderer) Text(s string) {
	if s == "" {
		return
	}
	if r.reasoningOpen {
		fmt.Fprintln(r.out)
		r.reasoningOpen = false
	}
	if !r.textStarted {
		if r.ui {
			fmt.Fprint(r.out, r.paint(ansiCyan, "assistant › "))
		}
		r.textStarted = true
	}
	fmt.Fprint(r.out, s)
}

// Reasoning streams reasoning_content (DeepSeek reasoner) in dim text.
func (r *renderer) Reasoning(s string) {
	if s == "" || !r.showReasoning {
		return
	}
	if r.textStarted || !r.reasoningStarted {
		if r.textStarted {
			fmt.Fprintln(r.out)
			r.textStarted = false
		}
		if !r.reasoningStarted {
			fmt.Fprint(r.out, r.paint(ansiDim, "thinking › "))
			r.reasoningStarted = true
		}
	}
	r.reasoningOpen = true
	fmt.Fprint(r.out, r.paint(ansiDim, s))
}

// ToolStart is called before a tool executes.
func (r *renderer) ToolStart(name, args string) {
	if r.quiet {
		return
	}
	r.endLine()
	fmt.Fprintf(r.diag, "  %s %s %s\n",
		r.paint(ansiYellow, "[tool]"),
		name,
		r.dim(oneLine(args, 200)))
}

// ToolEnd is called after a tool executes.
func (r *renderer) ToolEnd(name, output string, err error, elapsed time.Duration) {
	if r.quiet {
		return
	}
	label := r.paint(ansiGreen, "[ok]")
	if err != nil {
		label = r.paint(ansiRed, "[tool-error]")
	}
	fmt.Fprintf(r.diag, "  %s %s %s %s\n",
		label, name, r.dim(oneLine(output, 200)), r.dim("("+shortDuration(elapsed)+")"))
}

// Retry reports an automatic retry.
func (r *renderer) Retry(info llm.RetryInfo) {
	if r.quiet {
		return
	}
	r.endLine()
	fmt.Fprintf(r.diag, "  %s %s, retry %d/%d in %s\n",
		r.paint(ansiYellow, "[retry]"),
		info.Reason,
		info.Attempt,
		info.MaxAttempts-1,
		shortDuration(info.Delay))
}

// Request prints per-request accounting when verbose.
func (r *renderer) Request(info agent.RequestInfo, price usage.Price, hasPrice bool) {
	if !r.verbose || r.quiet {
		return
	}
	r.endLine()
	line := tokensLine(info.Tokens, price, hasPrice)
	if info.Attempts > 1 {
		line += fmt.Sprintf(" · %d attempts", info.Attempts)
	}
	if info.ToolCalls > 0 {
		line += " · " + count(info.ToolCalls, "tool call")
	}
	fmt.Fprintf(r.diag, "  %s %s %s %s\n",
		r.dim("[req]"),
		r.dim(info.Model),
		line,
		r.dim("("+shortDuration(info.Elapsed)+")"))
}

// Repair reports a schema-validation failure that triggers a repair request.
func (r *renderer) Repair(errs []string, attempt int) {
	r.endLine()
	msg := strings.Join(errs, "; ")
	fmt.Fprintf(r.diag, "  %s schema validation failed (attempt %d): %s\n",
		r.paint(ansiYellow, "[repair]"), attempt, oneLine(msg, 400))
}

// Structured prints the validated JSON on stdout and a status line on stderr.
func (r *renderer) Structured(raw json.RawMessage, repairs int) {
	if len(raw) == 0 {
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		buf.Write(raw)
	}
	if repairs > 0 && !r.quiet {
		fmt.Fprintf(r.diag, "  %s validated after %d repair(s)\n", r.paint(ansiGreen, "[json]"), repairs)
	}
	fmt.Fprintln(r.out, buf.String())
}

// TurnSummary reports the aggregate cost/token line for one user turn.
func (r *renderer) TurnSummary(res *agent.TurnResult, price usage.Price, hasPrice bool) {
	if res == nil {
		return
	}
	r.endLine()
	line := tokensLine(res.Stats.Tokens, price, hasPrice)
	parts := []string{line}
	if res.Requests > 1 {
		parts = append(parts, count(res.Requests, "request"))
	}
	if res.ToolCalls > 0 {
		parts = append(parts, count(res.ToolCalls, "tool call"))
	}
	if res.Repairs > 0 {
		parts = append(parts, count(res.Repairs, "repair"))
	}
	parts = append(parts, shortDuration(res.Duration))
	fmt.Fprintf(r.diag, "  %s %s\n", r.dim("[turn]"), r.dim(strings.Join(parts, " · ")))
}

// SessionSummary reports cumulative accounting (used by /cost and at exit).
func (r *renderer) SessionSummary(stats usage.Stats, price usage.Price, hasPrice bool) {
	r.endLine()
	line := tokensLine(stats.Tokens, price, hasPrice)
	fmt.Fprintf(r.diag, "  %s %s · %s · %s · %s\n",
		r.dim("[session]"), line,
		count(stats.Requests, "request"),
		count(stats.ToolCalls, "tool call"),
		count(stats.Repairs, "repair"))
}

// Error prints a classified error with a hint.
func (r *renderer) Error(err error) {
	if err == nil {
		return
	}
	r.endLine()
	var le *llm.Error
	if errors.As(err, &le) {
		if le.Kind == llm.KindCanceled {
			fmt.Fprintf(r.diag, "  %s\n", r.dim("[interrupted] 已取消当前请求"))
			return
		}
		fmt.Fprintf(r.diag, "  %s %s\n", r.paint(ansiRed, "[error]"), le.Summary())
		fmt.Fprintf(r.diag, "          %s\n", oneLine(le.Error(), 400))
		if hint := le.Hint(); hint != "" {
			fmt.Fprintf(r.diag, "          %s\n", r.dim(hint))
		}
		return
	}
	var se *agent.StructuredError
	if errors.As(err, &se) {
		fmt.Fprintf(r.diag, "  %s structured output failed after %d attempt(s)\n", r.paint(ansiRed, "[error]"), se.Attempts)
		for i, e := range se.Errors {
			if i >= 5 {
				fmt.Fprintf(r.diag, "          ... (%d more)\n", len(se.Errors)-5)
				break
			}
			fmt.Fprintf(r.diag, "          - %s\n", e)
		}
		return
	}
	var mt *agent.MaxTurnsError
	if errors.As(err, &mt) {
		fmt.Fprintf(r.diag, "  %s %s\n", r.paint(ansiRed, "[error]"), mt.Error())
		fmt.Fprintf(r.diag, "          %s\n", r.dim("可能是工具调用死循环；可用 -max-turns 调整上限，或换一种问法。"))
		return
	}
	fmt.Fprintf(r.diag, "  %s %s\n", r.paint(ansiRed, "[error]"), err.Error())
}

func (r *renderer) endLine() {
	if r.textStarted || r.reasoningOpen {
		fmt.Fprintln(r.out)
		r.textStarted = false
		r.reasoningOpen = false
		r.reasoningStarted = false
	}
}

func tokensLine(t usage.Tokens, price usage.Price, hasPrice bool) string {
	var parts []string
	in := fmt.Sprintf("in %s", usage.Comma(t.Prompt))
	if t.Cached > 0 {
		in += fmt.Sprintf(" (cache %s)", usage.Comma(t.Cached))
	}
	parts = append(parts, in)
	out := fmt.Sprintf("out %s", usage.Comma(t.Completion))
	if t.Reasoning > 0 {
		out += fmt.Sprintf(" (reasoning %s)", usage.Comma(t.Reasoning))
	}
	parts = append(parts, out)
	if hasPrice {
		parts = append(parts, "est "+usage.FormatUSD(price.Estimate(t).Total))
	} else {
		parts = append(parts, "est n/a")
	}
	return strings.Join(parts, " · ")
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len([]rune(s)) > max {
		runes := []rune(s)
		return string(runes[:max]) + "…"
	}
	return s
}

func count(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
