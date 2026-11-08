package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "time/tzdata" // make IANA timezones work on Windows

	"agentcli/internal/version"
)

type config struct {
	model   string
	baseURL string
	apiKey  string
	system  string

	timeout     time.Duration
	turnTimeout time.Duration

	maxTurns    int
	maxRetries  int
	maxRepairs  int
	maxTokens   int
	temperature float64

	schemaPath string
	schemaName string
	jsonMode   string

	toolsList     string
	noTools       bool
	workdir       string
	maxToolOutput int

	showReasoning bool
	verbose       bool
	quiet         bool
	colorMode     string

	priceIn     float64
	priceCached float64
	priceOut    float64
}

func defaultConfig() config {
	model := envOr("DEEPSEEK_MODEL", "AGENT_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	baseURL := envOr("DEEPSEEK_BASE_URL", "OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	return config{
		model:         model,
		baseURL:       baseURL,
		apiKey:        envOr("DEEPSEEK_API_KEY", "OPENAI_API_KEY"),
		timeout:       120 * time.Second,
		turnTimeout:   5 * time.Minute,
		maxTurns:      8,
		maxRetries:    2,
		maxRepairs:    2,
		temperature:   1.0,
		jsonMode:      "auto",
		toolsList:     "get_current_time,calculator,read_file",
		workdir:       ".",
		maxToolOutput: 8000,
		showReasoning: false,
		colorMode:     "auto",
		schemaName:    "response",
		priceIn:       -1,
		priceCached:   -1,
		priceOut:      -1,
	}
}

func envOr(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func main() {
	initConsole()
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	askMode := false
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printUsage(stderr)
			return 0
		case "version", "-version", "--version":
			fmt.Fprintf(stdout, "agent %s\n", version.String())
			return 0
		case "tools":
			return listTools(stdout, stderr)
		case "ask":
			askMode = true
			args = args[1:]
		}
	}

	cfg := defaultConfig()

	var apiKeyFlag string
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs, &cfg, &apiKeyFlag)

	// flag 和问题可以任意顺序：`agent --no-tools "hi"` 与 `agent "hi" --no-tools` 都成立。
	question, flagArgs := splitQuestion(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stderr)
			return 0
		}
		fmt.Fprintf(stderr, "参数错误：%v\n\n", err)
		printUsage(stderr)
		return 2
	}
	if apiKeyFlag != "" {
		cfg.apiKey = apiKeyFlag
	}
	if rest := strings.Join(fs.Args(), " "); rest != "" {
		if strings.TrimSpace(question) == "" {
			question = rest
		} else {
			question += " " + rest
		}
	}
	if askMode && strings.TrimSpace(question) == "" {
		fmt.Fprintln(stderr, "缺少问题：用法 agent ask \"你的问题\"")
		return 2
	}
	if err := validateConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "参数错误：%v\n", err)
		return 2
	}

	app, err := newApp(cfg, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return 2
	}

	if strings.TrimSpace(question) != "" {
		return app.runSingle(question)
	}
	return app.runREPL(stdin)
}

func registerFlags(fs *flag.FlagSet, cfg *config, apiKeyFlag *string) {
	fs.StringVar(&cfg.model, "model", cfg.model, "模型名（默认取 DEEPSEEK_MODEL，否则 deepseek-chat）")
	fs.StringVar(&cfg.baseURL, "base-url", cfg.baseURL, "OpenAI 兼容 API 地址（默认取 DEEPSEEK_BASE_URL / OPENAI_BASE_URL）")
	fs.StringVar(apiKeyFlag, "api-key", "", "API Key（也可用 DEEPSEEK_API_KEY / OPENAI_API_KEY）")
	fs.StringVar(&cfg.system, "system", "", "自定义 system prompt")
	fs.DurationVar(&cfg.timeout, "timeout", cfg.timeout, "单次 HTTP 请求超时（含流式读取）")
	fs.DurationVar(&cfg.turnTimeout, "turn-timeout", cfg.turnTimeout, "单轮对话总超时（含工具调用），0 表示不限制")
	fs.IntVar(&cfg.maxTurns, "max-turns", cfg.maxTurns, "单轮最多请求模型次数（防工具死循环）")
	fs.IntVar(&cfg.maxRetries, "max-retries", cfg.maxRetries, "可重试错误的最大重试次数")
	fs.IntVar(&cfg.maxRepairs, "max-repairs", cfg.maxRepairs, "结构化输出校验失败后的修复次数")
	fs.IntVar(&cfg.maxTokens, "max-tokens", cfg.maxTokens, "max_tokens，0 表示不设置")
	fs.Float64Var(&cfg.temperature, "temperature", cfg.temperature, "temperature；传 -1 表示不发送该参数")
	fs.StringVar(&cfg.schemaPath, "schema", "", "JSON Schema 文件路径，启用结构化输出")
	fs.StringVar(&cfg.schemaName, "schema-name", cfg.schemaName, "结构化输出的 schema 名称")
	fs.StringVar(&cfg.jsonMode, "json-mode", cfg.jsonMode, "结构化输出模式：auto|object|schema|none")
	fs.StringVar(&cfg.toolsList, "tools", cfg.toolsList, "启用的工具，逗号分隔")
	fs.BoolVar(&cfg.noTools, "no-tools", false, "禁用所有工具")
	fs.StringVar(&cfg.workdir, "workdir", cfg.workdir, "read_file 工具的根目录")
	fs.IntVar(&cfg.maxToolOutput, "max-tool-output", cfg.maxToolOutput, "单个工具结果的最大字符数")
	fs.BoolVar(&cfg.showReasoning, "show-reasoning", false, "显示 reasoning_content（DeepSeek reasoner）")
	fs.BoolVar(&cfg.verbose, "verbose", false, "打印每个请求的 token / 重试 / 耗时明细")
	fs.BoolVar(&cfg.quiet, "quiet", false, "隐藏工具与重试提示（保留 turn 统计）")
	fs.StringVar(&cfg.colorMode, "color", cfg.colorMode, "颜色：auto|always|never")
	fs.Float64Var(&cfg.priceIn, "price-in", cfg.priceIn, "输入价格（USD / 1M tokens），-1 用内置价格表")
	fs.Float64Var(&cfg.priceCached, "price-cached", cfg.priceCached, "缓存命中输入价格（USD / 1M tokens）")
	fs.Float64Var(&cfg.priceOut, "price-out", cfg.priceOut, "输出价格（USD / 1M tokens）")
}

func validateConfig(cfg config) error {
	switch cfg.jsonMode {
	case "auto", "object", "schema", "none":
	default:
		return fmt.Errorf("-json-mode 必须是 auto|object|schema|none，收到 %q", cfg.jsonMode)
	}
	switch cfg.colorMode {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("-color 必须是 auto|always|never，收到 %q", cfg.colorMode)
	}
	if cfg.maxTurns < 1 {
		return fmt.Errorf("-max-turns 必须 >= 1")
	}
	if cfg.maxRetries < 0 {
		return fmt.Errorf("-max-retries 必须 >= 0")
	}
	if cfg.maxRepairs < 0 {
		return fmt.Errorf("-max-repairs 必须 >= 0")
	}
	if cfg.jsonMode != "auto" && cfg.schemaPath == "" {
		return fmt.Errorf("-json-mode 需要配合 -schema 使用")
	}
	if cfg.timeout <= 0 {
		return fmt.Errorf("-timeout 必须 > 0")
	}
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `agent %s — 流式 LLM CLI（OpenAI 兼容 / DeepSeek）

用法：
  agent [flags] "问题"         单轮提问，流式输出
  agent ask [flags] "问题"     同上
  agent [flags]                进入交互式 REPL
  agent tools                  列出内置工具
  agent version                版本
  agent help                   帮助

示例：
  # DeepSeek（需要 DEEPSEEK_API_KEY）
  agent "用一句话解释 SSE 流式输出"

  # 本地 mock，不需要网络和 Key
  go run ./cmd/mockllm
  agent --base-url http://127.0.0.1:18080/v1 --api-key mock "现在几点？"

  # 结构化输出
  agent -schema ./schema/person.schema.json "从这句话提取姓名和年龄：Ada 36 岁"

  # 关闭工具，强制直接回答
  agent -no-tools "1+1 等于几"

环境变量：
  DEEPSEEK_API_KEY / OPENAI_API_KEY
  DEEPSEEK_BASE_URL / OPENAI_BASE_URL
  DEEPSEEK_MODEL

交互命令：
  /help /reset /tools /cost /model <id> /schema <path|off>
  /system <text> /verbose /reasoning /exit

参数：
`, version.String())
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(w)
	dummy := defaultConfig()
	var key string
	registerFlags(fs, &dummy, &key)
	fs.PrintDefaults()
}

func listTools(stdout, stderr io.Writer) int {
	reg, err := buildRegistry(defaultConfig())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if reg == nil {
		fmt.Fprintln(stdout, "(tools disabled)")
		return 0
	}
	for _, t := range reg.All() {
		fmt.Fprintf(stdout, "%-18s %s\n", t.Name, t.Description)
	}
	return 0
}

// splitQuestion separates the question text from flags so that both
// `agent --no-tools "hi"` and `agent "hi" --no-tools` work. Unquoted
// multi-word questions stay intact as long as no word starts with '-'.
//
// A flag that takes a value consumes the next argument; boolean flags and
// `-flag=value` forms do not. Flags are recognized by their registration, so
// an unknown flag still reaches Parse and produces the usual error.
func splitQuestion(fs *flag.FlagSet, args []string) (question string, flags []string) {
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			words = append(words, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue // -flag=value carries its own value
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown: let Parse report it
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue // -verbose 不带值
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return strings.Join(words, " "), flags
}
