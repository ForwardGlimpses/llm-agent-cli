package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"

	"agentcli/internal/agent"
	"agentcli/internal/llm"
	"agentcli/internal/schema"
	"agentcli/internal/tools"
	"agentcli/internal/usage"
	"agentcli/internal/version"
)

type app struct {
	cfg    config
	out    io.Writer
	diag   io.Writer
	render *renderer

	client   *llm.Client
	registry *tools.Registry
	schema   *schema.Schema
	format   *openai.ChatCompletionNewParamsResponseFormatUnion
	agent    *agent.Agent

	price   usage.Price
	priceOK bool
}

func newApp(cfg config, out, diag io.Writer) (*app, error) {
	a := &app{cfg: cfg, out: out, diag: diag}
	ui := isTerminalWriter(out)
	a.render = newRenderer(out, diag, ui, colorEnabled(cfg.colorMode, ui), cfg.showReasoning, cfg.verbose, cfg.quiet)

	if cfg.schemaPath != "" {
		s, err := schema.ParseFile(cfg.schemaPath)
		if err != nil {
			return nil, err
		}
		a.schema = s
	}

	reg, err := buildRegistry(cfg)
	if err != nil {
		return nil, err
	}
	a.registry = reg

	temp := cfg.temperature
	var tempPtr *float64
	if temp >= 0 {
		tempPtr = &temp
	}
	a.client = llm.New(llm.Config{
		BaseURL:        cfg.baseURL,
		APIKey:         cfg.apiKey,
		Model:          cfg.model,
		Temperature:    tempPtr,
		MaxTokens:      cfg.maxTokens,
		RequestTimeout: cfg.timeout,
		MaxRetries:     cfg.maxRetries,
		BackoffBase:    500 * time.Millisecond,
		BackoffMax:     8 * time.Second,
	})
	if err := a.buildFormat(); err != nil {
		return nil, err
	}
	a.refreshPrice()
	a.rebuildAgent()

	if cfg.apiKey == "" && (strings.Contains(cfg.baseURL, "deepseek") || strings.Contains(cfg.baseURL, "openai.com")) {
		fmt.Fprintln(diag, a.render.dim("[warn] 未设置 API Key：请设置 DEEPSEEK_API_KEY 或 OPENAI_API_KEY。"))
	}
	return a, nil
}

func buildRegistry(cfg config) (*tools.Registry, error) {
	if cfg.noTools {
		return nil, nil
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(cfg.toolsList, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			wanted[name] = true
		}
	}
	reg := tools.NewRegistry(cfg.maxToolOutput)
	found := map[string]bool{}
	available := []string{}
	for _, t := range tools.Builtins(cfg.workdir) {
		available = append(available, t.Name)
		if len(wanted) > 0 && !wanted[t.Name] {
			continue
		}
		if err := reg.Add(t); err != nil {
			return nil, err
		}
		found[t.Name] = true
	}
	for name := range wanted {
		if !found[name] {
			return nil, fmt.Errorf("未知工具 %q（可用：%s）", name, strings.Join(available, ", "))
		}
	}
	return reg, nil
}

func (a *app) buildFormat() error {
	if a.schema == nil {
		a.format = nil
		return nil
	}
	mode := a.cfg.jsonMode
	if mode == "auto" {
		switch {
		case strings.Contains(strings.ToLower(a.cfg.model), "reasoner"):
			// Reasoning models are the most conservative: prompt-only JSON,
			// validated locally, avoids provider-specific response_format rules.
			mode = "none"
		case isDeepSeek(a.cfg.baseURL, a.cfg.model):
			mode = "object"
		default:
			mode = "schema"
		}
	}
	switch mode {
	case "object":
		p := shared.NewResponseFormatJSONObjectParam()
		a.format = &openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &p}
	case "schema":
		raw := a.schema.Raw()
		clean := make(map[string]any, len(raw))
		for k, v := range raw {
			// OpenAI's structured outputs reject/ignore the meta-schema key.
			if k == "$schema" {
				continue
			}
			clean[k] = v
		}
		a.format = &openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				Type: "json_schema",
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   a.cfg.schemaName,
					Schema: clean,
				},
			},
		}
	case "none":
		a.format = nil
	default:
		return fmt.Errorf("未知 json-mode %q", mode)
	}
	return nil
}

func isDeepSeek(baseURL, model string) bool {
	return strings.Contains(strings.ToLower(baseURL), "deepseek") ||
		strings.HasPrefix(strings.ToLower(model), "deepseek")
}

func (a *app) refreshPrice() {
	p, ok := usage.LookupPrice(a.cfg.model)
	if a.cfg.priceIn >= 0 {
		p.InputPerM = a.cfg.priceIn
		ok = true
	}
	if a.cfg.priceCached >= 0 {
		p.CachedInputPerM = a.cfg.priceCached
		ok = true
	}
	if a.cfg.priceOut >= 0 {
		p.OutputPerM = a.cfg.priceOut
		ok = true
	}
	a.price, a.priceOK = p, ok
}

func (a *app) rebuildAgent() {
	hooks := agent.Hooks{
		OnRetry:   a.render.Retry,
		OnRequest: func(info agent.RequestInfo) { a.render.Request(info, a.price, a.priceOK) },
		OnToolStart: func(name, args string) {
			a.render.ToolStart(name, args)
		},
		OnToolEnd: func(name, output string, err error, elapsed time.Duration) {
			a.render.ToolEnd(name, output, err, elapsed)
		},
		OnRepair: a.render.Repair,
	}
	if a.schema == nil {
		hooks.OnText = a.render.Text
		hooks.OnReasoning = a.render.Reasoning
	}
	a.agent = agent.New(a.client, a.registry, agent.Options{
		System:         a.cfg.system,
		MaxTurns:       a.cfg.maxTurns,
		MaxRepairs:     a.cfg.maxRepairs,
		Schema:         a.schema,
		ResponseFormat: a.format,
		ToolTimeout:    30 * time.Second,
		Hooks:          hooks,
	})
}

func (a *app) summarize(res *agent.TurnResult) {
	if res == nil {
		return
	}
	if res.Stats.Total == 0 && res.Content == "" && res.ToolCalls == 0 {
		return
	}
	a.render.TurnSummary(res, a.price, a.priceOK)
}

func (a *app) sessionSummary() {
	stats := a.agent.Total()
	if stats.Requests == 0 {
		return
	}
	a.render.SessionSummary(stats, a.price, a.priceOK)
}

func (a *app) runSingle(question string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if a.cfg.turnTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.cfg.turnTimeout)
		defer cancel()
	}

	a.render.StartTurn()
	res, err := a.agent.Run(ctx, question)
	a.summarize(res)
	if err != nil {
		a.render.Error(err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	if a.schema != nil {
		a.render.Structured(res.Structured, res.Repairs)
	}
	return 0
}

func (a *app) runREPL(stdin io.Reader) int {
	ctx := context.Background()
	state := &interruptState{}
	go watchInterrupts(state, a.diag)

	if !a.cfg.quiet {
		a.printBanner()
	}
	reader := bufio.NewReaderSize(stdin, 1<<20)
	interactive := isTerminalWriter(a.out)
	for {
		if interactive {
			fmt.Fprint(a.out, a.render.paint(ansiGreen, "you › "))
		}
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(a.diag, "读取输入失败：%v\n", err)
			return 1
		}
		input := strings.TrimSpace(line)
		if input != "" {
			if strings.HasPrefix(input, "/") {
				if a.handleCommand(input) {
					break
				}
			} else {
				turnCtx := ctx
				var cancel context.CancelFunc
				if a.cfg.turnTimeout > 0 {
					turnCtx, cancel = context.WithTimeout(ctx, a.cfg.turnTimeout)
				} else {
					turnCtx, cancel = context.WithCancel(ctx)
				}
				state.begin(cancel)
				a.render.StartTurn()
				res, rerr := a.agent.Run(turnCtx, input)
				state.end()
				cancel()
				a.summarize(res)
				if rerr != nil {
					a.render.Error(rerr)
				} else if a.schema != nil && res != nil {
					a.render.Structured(res.Structured, res.Repairs)
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	a.sessionSummary()
	return 0
}

func (a *app) printBanner() {
	toolsLabel := "disabled"
	if a.registry != nil && len(a.registry.Names()) > 0 {
		toolsLabel = fmt.Sprintf("%d (%s)", len(a.registry.Names()), strings.Join(a.registry.Names(), ", "))
	}
	structured := "off"
	if a.schema != nil {
		structured = "on (" + a.cfg.jsonMode + ")"
	}
	fmt.Fprintf(a.diag, "%s model=%s base=%s\n", a.render.paint(ansiCyan, "agent "+version.String()), a.cfg.model, a.cfg.baseURL)
	fmt.Fprintf(a.diag, "  tools=%s · structured=%s · timeout=%s\n", toolsLabel, structured, a.cfg.timeout)
	fmt.Fprintln(a.diag, a.render.dim("  输入问题开始；/help 查看命令；Ctrl+C 取消当前请求。"))
}

func (a *app) handleCommand(line string) bool {
	fields := strings.Fields(line)
	cmd := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	switch cmd {
	case "/exit", "/quit", "/q":
		return true
	case "/help":
		fmt.Fprint(a.diag, `命令：
  /help                 显示帮助
  /reset                清空会话（保留 system prompt）
  /tools                列出工具
  /cost                 本次会话累计 token 与费用
  /model <id>           切换模型（会重置会话）
  /schema <path|off>    启用/关闭结构化输出（会重置会话）
  /system <text>        设置 system prompt（会重置会话）
  /verbose              切换每请求明细
  /reasoning            切换 reasoning_content 显示
  /price                当前模型价格
  /exit                 退出
`)
	case "/reset":
		a.rebuildAgent()
		fmt.Fprintln(a.diag, a.render.dim("[ok] 会话已重置"))
	case "/tools":
		if a.registry == nil {
			fmt.Fprintln(a.diag, "工具已禁用")
			break
		}
		for _, t := range a.registry.All() {
			fmt.Fprintf(a.diag, "  %-18s %s\n", t.Name, t.Description)
		}
	case "/cost":
		a.sessionSummary()
	case "/model":
		if rest == "" {
			fmt.Fprintln(a.diag, "用法：/model <id>")
			break
		}
		a.cfg.model = rest
		a.client.SetModel(rest)
		a.refreshPrice()
		if err := a.buildFormat(); err != nil {
			a.render.Error(err)
			break
		}
		a.rebuildAgent()
		fmt.Fprintf(a.diag, "[ok] 模型切换为 %s，会话已重置\n", rest)
	case "/schema":
		if rest == "" {
			fmt.Fprintln(a.diag, "用法：/schema <path|off>")
			break
		}
		if strings.EqualFold(rest, "off") {
			a.schema = nil
			a.cfg.schemaPath = ""
		} else {
			s, err := schema.ParseFile(rest)
			if err != nil {
				a.render.Error(err)
				break
			}
			a.schema = s
			a.cfg.schemaPath = rest
		}
		if err := a.buildFormat(); err != nil {
			a.render.Error(err)
			break
		}
		a.rebuildAgent()
		state := "关闭"
		if a.schema != nil {
			state = "启用"
		}
		fmt.Fprintf(a.diag, "[ok] 结构化输出已%s，会话已重置\n", state)
	case "/system":
		a.cfg.system = rest
		a.rebuildAgent()
		fmt.Fprintln(a.diag, "[ok] system prompt 已更新，会话已重置")
	case "/verbose":
		a.cfg.verbose = !a.cfg.verbose
		a.render.verbose = a.cfg.verbose
		fmt.Fprintf(a.diag, "[ok] verbose=%v\n", a.cfg.verbose)
	case "/reasoning":
		a.cfg.showReasoning = !a.cfg.showReasoning
		a.render.showReasoning = a.cfg.showReasoning
		fmt.Fprintf(a.diag, "[ok] show-reasoning=%v\n", a.cfg.showReasoning)
	case "/price":
		if !a.priceOK {
			fmt.Fprintf(a.diag, "模型 %s 未配置价格；可用 -price-in/-price-out 指定（USD/1M tokens）\n", a.cfg.model)
			break
		}
		fmt.Fprintf(a.diag, "%s: in %g · cached %g · out %g USD/1M tokens\n",
			a.cfg.model, a.price.InputPerM, a.price.CachedInputPerM, a.price.OutputPerM)
	default:
		fmt.Fprintf(a.diag, "未知命令 %s，输入 /help 查看帮助\n", cmd)
	}
	return false
}

type interruptState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	busy   bool
}

func (s *interruptState) begin(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
	s.busy = true
}

func (s *interruptState) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	s.cancel = nil
}

func watchInterrupts(state *interruptState, diag io.Writer) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	for range ch {
		state.mu.Lock()
		if state.busy && state.cancel != nil {
			cancel := state.cancel
			state.cancel = nil
			state.busy = false
			state.mu.Unlock()
			cancel()
			fmt.Fprintln(diag, "\n[interrupt] 已取消当前请求（再按 Ctrl+C 退出）")
			continue
		}
		state.mu.Unlock()
		fmt.Fprintln(diag, "\n[exit]")
		os.Exit(130)
	}
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func colorEnabled(mode string, ui bool) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if !ui || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return os.Getenv("TERM") != "" || os.Getenv("WT_SESSION") != "" || os.Getenv("ConEmuANSI") == "ON"
}
