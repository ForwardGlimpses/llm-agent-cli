package usage

import "testing"

func TestEstimateWithCachedTokens(t *testing.T) {
	p := Price{InputPerM: 1.0, CachedInputPerM: 0.1, OutputPerM: 2.0}
	c := p.Estimate(Tokens{Prompt: 1_000_000, Completion: 500_000, Cached: 400_000})
	if c.Input < 0.599999 || c.Input > 0.600001 {
		t.Errorf("input = %v, want 0.6", c.Input)
	}
	if c.CachedInput < 0.039999 || c.CachedInput > 0.040001 {
		t.Errorf("cached = %v, want 0.04", c.CachedInput)
	}
	if c.Output != 1.0 {
		t.Errorf("output = %v, want 1.0", c.Output)
	}
	if c.Total < 1.639999 || c.Total > 1.640001 {
		t.Errorf("total = %v", c.Total)
	}
}

func TestEstimateClampsCachedToPrompt(t *testing.T) {
	p := Price{InputPerM: 1, CachedInputPerM: 0, OutputPerM: 0}
	c := p.Estimate(Tokens{Prompt: 10, Cached: 100})
	if c.Input != 0 || c.CachedInput <= 0 {
		t.Errorf("cost = %+v", c)
	}
}

func TestLookupPricePrefersLongestPrefix(t *testing.T) {
	mini, ok := LookupPrice("gpt-4o-mini-2024-07-18")
	if !ok {
		t.Fatal("not found")
	}
	full, _ := LookupPrice("gpt-4o")
	if mini.InputPerM >= full.InputPerM {
		t.Errorf("mini price should be lower: %+v vs %+v", mini, full)
	}
	if _, ok := LookupPrice("some-local-model"); ok {
		t.Error("unknown model should not match")
	}
	if p, ok := LookupPrice("deepseek/deepseek-chat"); !ok || p.OutputPerM != 0.42 {
		t.Errorf("provider-qualified lookup failed: %+v %v", p, ok)
	}
	if p, ok := LookupPrice("gpt-4.1-nano"); !ok || p.InputPerM != 0.10 {
		t.Errorf("nano should beat the gpt-4.1 prefix: %+v %v", p, ok)
	}
}

func TestStatsDelta(t *testing.T) {
	var s Stats
	s.AddTokens(Tokens{Prompt: 10, Completion: 5, Total: 15})
	prev := s
	s.AddTokens(Tokens{Prompt: 20, Completion: 7, Total: 27})
	d := s.Delta(prev)
	if d.Prompt != 20 || d.Completion != 7 || d.Total != 27 || d.Requests != 1 {
		t.Errorf("delta = %+v", d)
	}
}

func TestComma(t *testing.T) {
	cases := map[int64]string{0: "0", 12: "12", 1234: "1,234", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := Comma(in); got != want {
			t.Errorf("Comma(%d) = %q, want %q", in, got, want)
		}
	}
}
