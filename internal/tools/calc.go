package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CalculatorTool exposes a safe arithmetic evaluator. There is deliberately no
// eval: the recursive-descent parser only accepts numbers, parentheses,
// arithmetic operators and a fixed set of functions.
func CalculatorTool() *Tool {
	return &Tool{
		Name:        "calculator",
		Description: "Evaluate a mathematical expression. Supports + - * / % ^, parentheses, pi/e and functions sqrt, abs, sin, cos, tan, log, log10, exp, min, max, pow, round, floor, ceil. Use this instead of doing arithmetic in your head.",
		Parameters: ObjectSchema(map[string]any{
			"expression": map[string]any{
				"type":        "string",
				"description": "Expression to evaluate, e.g. \"(1234.5 * 0.07) ^ 2\".",
			},
		}, "expression"),
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Expression string `json:"expression"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Expression) == "" {
				return "", fmt.Errorf("expression is empty")
			}
			value, err := Eval(args.Expression)
			if err != nil {
				return "", err
			}
			res := map[string]any{
				"expression": args.Expression,
				"result":     value,
			}
			b, _ := json.Marshal(res)
			return string(b), nil
		},
	}
}

// Eval parses and evaluates an arithmetic expression.
func Eval(expr string) (float64, error) {
	p := &calcParser{s: expr}
	p.skipSpace()
	if p.eof() {
		return 0, fmt.Errorf("empty expression")
	}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if !p.eof() {
		return 0, p.errf("unexpected %q", p.s[p.pos])
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("result is not a finite number")
	}
	return v, nil
}

type calcParser struct {
	s   string
	pos int
}

func (p *calcParser) eof() bool { return p.pos >= len(p.s) }

func (p *calcParser) skipSpace() {
	for !p.eof() {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *calcParser) errf(format string, args ...any) error {
	return fmt.Errorf("position %d: %s", p.pos, fmt.Sprintf(format, args...))
}

func (p *calcParser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.eof() {
			return left, nil
		}
		op := p.s[p.pos]
		if op != '+' && op != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *calcParser) parseTerm() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.eof() {
			return left, nil
		}
		op := p.s[p.pos]
		if op != '*' && op != '/' && op != '%' {
			return left, nil
		}
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, p.errf("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, p.errf("modulo by zero")
			}
			left = math.Mod(left, right)
		}
	}
}

func (p *calcParser) parseUnary() (float64, error) {
	p.skipSpace()
	if p.eof() {
		return 0, p.errf("unexpected end of expression")
	}
	switch p.s[p.pos] {
	case '+':
		p.pos++
		return p.parseUnary()
	case '-':
		p.pos++
		v, err := p.parseUnary()
		return -v, err
	}
	return p.parsePower()
}

func (p *calcParser) parsePower() (float64, error) {
	base, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if !p.eof() && p.s[p.pos] == '^' {
		p.pos++
		exp, err := p.parseUnary() // right associative: 2^3^2 = 2^(3^2)
		if err != nil {
			return 0, err
		}
		v := math.Pow(base, exp)
		if math.IsNaN(v) {
			return 0, p.errf("power is undefined for these operands")
		}
		return v, nil
	}
	return base, nil
}

func (p *calcParser) parsePrimary() (float64, error) {
	p.skipSpace()
	if p.eof() {
		return 0, p.errf("unexpected end of expression")
	}
	c := p.s[p.pos]
	if c == '(' {
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.eof() || p.s[p.pos] != ')' {
			return 0, p.errf("missing closing parenthesis")
		}
		p.pos++
		return v, nil
	}
	if isDigit(c) || c == '.' {
		return p.parseNumber()
	}
	if isIdentStart(c) {
		return p.parseIdent()
	}
	return 0, p.errf("unexpected character %q", c)
}

func (p *calcParser) parseNumber() (float64, error) {
	start := p.pos
	for !p.eof() && isDigit(p.s[p.pos]) {
		p.pos++
	}
	if !p.eof() && p.s[p.pos] == '.' {
		p.pos++
		for !p.eof() && isDigit(p.s[p.pos]) {
			p.pos++
		}
	}
	if !p.eof() && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		save := p.pos
		p.pos++
		if !p.eof() && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
			p.pos++
		}
		if p.eof() || !isDigit(p.s[p.pos]) {
			p.pos = save
		} else {
			for !p.eof() && isDigit(p.s[p.pos]) {
				p.pos++
			}
		}
	}
	text := p.s[start:p.pos]
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, p.errf("invalid number %q", text)
	}
	return v, nil
}

func (p *calcParser) parseIdent() (float64, error) {
	start := p.pos
	for !p.eof() && (isIdentStart(p.s[p.pos]) || isDigit(p.s[p.pos])) {
		p.pos++
	}
	name := p.s[start:p.pos]
	if p.eof() || p.s[p.pos] != '(' {
		switch name {
		case "pi":
			return math.Pi, nil
		case "e":
			return math.E, nil
		case "tau":
			return 2 * math.Pi, nil
		default:
			return 0, p.errf("unknown identifier %q", name)
		}
	}
	fn, ok := calcFuncs[name]
	if !ok {
		return 0, p.errf("unknown function %q", name)
	}
	p.pos++ // consume '('
	var args []float64
	p.skipSpace()
	if !p.eof() && p.s[p.pos] == ')' {
		p.pos++
	} else {
		for {
			v, err := p.parseExpr()
			if err != nil {
				return 0, err
			}
			args = append(args, v)
			p.skipSpace()
			if p.eof() {
				return 0, p.errf("missing closing parenthesis in %s(...)", name)
			}
			if p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.s[p.pos] == ')' {
				p.pos++
				break
			}
			return 0, p.errf("expected ',' or ')' in %s(...), got %q", name, p.s[p.pos])
		}
	}
	if len(args) < fn.minArgs || (fn.maxArgs >= 0 && len(args) > fn.maxArgs) {
		if fn.maxArgs < 0 {
			return 0, p.errf("%s expects at least %d argument(s), got %d", name, fn.minArgs, len(args))
		}
		return 0, p.errf("%s expects between %d and %d arguments, got %d", name, fn.minArgs, fn.maxArgs, len(args))
	}
	v := fn.fn(args)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, p.errf("%s returned a non-finite result", name)
	}
	return v, nil
}

type calcFunc struct {
	minArgs int
	maxArgs int // -1 means variadic
	fn      func([]float64) float64
}

var calcFuncs = map[string]calcFunc{
	"sqrt":  {1, 1, func(a []float64) float64 { return math.Sqrt(a[0]) }},
	"abs":   {1, 1, func(a []float64) float64 { return math.Abs(a[0]) }},
	"sin":   {1, 1, func(a []float64) float64 { return math.Sin(a[0]) }},
	"cos":   {1, 1, func(a []float64) float64 { return math.Cos(a[0]) }},
	"tan":   {1, 1, func(a []float64) float64 { return math.Tan(a[0]) }},
	"log":   {1, 1, func(a []float64) float64 { return math.Log(a[0]) }},
	"ln":    {1, 1, func(a []float64) float64 { return math.Log(a[0]) }},
	"log10": {1, 1, func(a []float64) float64 { return math.Log10(a[0]) }},
	"exp":   {1, 1, func(a []float64) float64 { return math.Exp(a[0]) }},
	"round": {1, 1, func(a []float64) float64 { return math.Round(a[0]) }},
	"floor": {1, 1, func(a []float64) float64 { return math.Floor(a[0]) }},
	"ceil":  {1, 1, func(a []float64) float64 { return math.Ceil(a[0]) }},
	"pow":   {2, 2, func(a []float64) float64 { return math.Pow(a[0], a[1]) }},
	"mod":   {2, 2, func(a []float64) float64 { return math.Mod(a[0], a[1]) }},
	"min":   {1, -1, func(a []float64) float64 { return minMax(a, false) }},
	"max":   {1, -1, func(a []float64) float64 { return minMax(a, true) }},
}

func minMax(a []float64, wantMax bool) float64 {
	v := a[0]
	for _, x := range a[1:] {
		if wantMax && x > v || !wantMax && x < v {
			v = x
		}
	}
	return v
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
