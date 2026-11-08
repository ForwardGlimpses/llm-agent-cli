// Package usage tracks token counts and estimates request cost.
package usage

import (
	"fmt"
	"strconv"
	"strings"

	"agentcli/internal/llm"
)

// Tokens mirrors the provider's usage object. Cached is a subset of Prompt.
type Tokens struct {
	Prompt     int64
	Completion int64
	Total      int64
	Cached     int64
	Reasoning  int64
}

// FromLLM converts the transport-level usage.
func FromLLM(t llm.Tokens) Tokens {
	total := t.Total
	if total == 0 {
		total = t.Prompt + t.Completion
	}
	return Tokens{
		Prompt:     t.Prompt,
		Completion: t.Completion,
		Total:      total,
		Cached:     t.Cached,
		Reasoning:  t.Reasoning,
	}
}

// Add accumulates v into t.
func (t *Tokens) Add(v Tokens) {
	total := v.Total
	if total == 0 {
		total = v.Prompt + v.Completion
	}
	t.Prompt += v.Prompt
	t.Completion += v.Completion
	t.Total += total
	t.Cached += v.Cached
	t.Reasoning += v.Reasoning
}

// Stats is session-level accounting.
type Stats struct {
	Tokens
	Requests  int
	ToolCalls int
	Repairs   int
}

// AddTokens records one request's usage.
func (s *Stats) AddTokens(t Tokens) {
	s.Tokens.Add(t)
	s.Requests++
}

// Delta returns the difference s-prev (used for per-turn reporting).
func (s Stats) Delta(prev Stats) Stats {
	return Stats{
		Tokens: Tokens{
			Prompt:     s.Prompt - prev.Prompt,
			Completion: s.Completion - prev.Completion,
			Total:      s.Total - prev.Total,
			Cached:     s.Cached - prev.Cached,
			Reasoning:  s.Reasoning - prev.Reasoning,
		},
		Requests:  s.Requests - prev.Requests,
		ToolCalls: s.ToolCalls - prev.ToolCalls,
		Repairs:   s.Repairs - prev.Repairs,
	}
}

// Price is USD per one million tokens.
type Price struct {
	InputPerM       float64
	CachedInputPerM float64
	OutputPerM      float64
}

// Cost is the estimated USD cost of a request.
type Cost struct {
	Input       float64
	CachedInput float64
	Output      float64
	Total       float64
}

// Estimate computes cost, treating Cached as a subset of Prompt. If no cached
// rate is configured, cached tokens are billed at the normal input rate.
func (p Price) Estimate(t Tokens) Cost {
	cached := t.Cached
	if cached < 0 {
		cached = 0
	}
	if cached > t.Prompt {
		cached = t.Prompt
	}
	uncached := t.Prompt - cached
	cachedRate := p.CachedInputPerM
	if cachedRate == 0 {
		cachedRate = p.InputPerM
	}
	c := Cost{
		Input:       float64(uncached) * p.InputPerM / 1e6,
		CachedInput: float64(cached) * cachedRate / 1e6,
		Output:      float64(t.Completion) * p.OutputPerM / 1e6,
	}
	c.Total = c.Input + c.CachedInput + c.Output
	return c
}

type priceEntry struct {
	prefix string
	price  Price
}

// The table is a best-effort default and may be stale: pricing changes. All
// values are USD per 1M tokens. Override them on the CLI with --price-in,
// --price-cached and --price-out.
//
// Keep longer prefixes first: gpt-4o-mini must win over gpt-4o.
var priceTable = []priceEntry{
	{"deepseek-chat", Price{InputPerM: 0.28, CachedInputPerM: 0.028, OutputPerM: 0.42}},
	{"deepseek-reasoner", Price{InputPerM: 0.28, CachedInputPerM: 0.028, OutputPerM: 0.42}},
	{"deepseek", Price{InputPerM: 0.28, CachedInputPerM: 0.028, OutputPerM: 0.42}},
	{"gpt-4o-mini", Price{InputPerM: 0.15, CachedInputPerM: 0.075, OutputPerM: 0.60}},
	{"gpt-4o", Price{InputPerM: 2.50, CachedInputPerM: 1.25, OutputPerM: 10.00}},
	{"gpt-4.1-nano", Price{InputPerM: 0.10, CachedInputPerM: 0.025, OutputPerM: 0.40}},
	{"gpt-4.1-mini", Price{InputPerM: 0.40, CachedInputPerM: 0.10, OutputPerM: 1.60}},
	{"gpt-4.1", Price{InputPerM: 2.00, CachedInputPerM: 0.50, OutputPerM: 8.00}},
	{"o3-mini", Price{InputPerM: 1.10, CachedInputPerM: 0.55, OutputPerM: 4.40}},
	{"o4-mini", Price{InputPerM: 1.10, CachedInputPerM: 0.275, OutputPerM: 4.40}},
}

// LookupPrice finds a price by exact prefix or provider-qualified model name
// (e.g. "deepseek/deepseek-chat"). Unknown models return false.
func LookupPrice(model string) (Price, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return Price{}, false
	}
	short := m
	if i := strings.LastIndex(m, "/"); i >= 0 {
		short = m[i+1:]
	}
	for _, entry := range priceTable {
		if strings.HasPrefix(m, entry.prefix) || strings.HasPrefix(short, entry.prefix) {
			return entry.price, true
		}
	}
	return Price{}, false
}

// FormatUSD renders a cost with enough precision for sub-cent requests.
func FormatUSD(v float64) string {
	if v <= 0 {
		return "$0"
	}
	if v < 0.000001 {
		return "<$0.000001"
	}
	if v < 0.01 {
		return fmt.Sprintf("$%.6f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}

// Comma formats an integer with thousands separators.
func Comma(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	start := len(s) % 3
	if start == 0 {
		start = 3
	}
	b.WriteString(s[:start])
	for i := start; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return sign + b.String()
}
