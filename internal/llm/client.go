package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

// Config controls the HTTP client. Zero values are replaced with sensible
// defaults; optional sampling fields use pointers so 0 can be expressed.
type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	Temperature    *float64
	MaxTokens      int
	RequestTimeout time.Duration
	MaxRetries     int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	HTTPClient     *http.Client
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.deepseek.com"
	}
	c.BaseURL = normalizeBaseURL(c.BaseURL)
	if c.Model == "" {
		c.Model = "deepseek-chat"
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 120 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 500 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 8 * time.Second
	}
	return c
}

func normalizeBaseURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if strings.Contains(s, "://") {
		return s
	}
	if strings.HasPrefix(s, "localhost") || strings.HasPrefix(s, "127.") || strings.HasPrefix(s, "0.0.0.0") {
		return "http://" + s
	}
	return "https://" + s
}

// Client wraps openai-go with our own retry loop. The SDK's built-in retries
// are disabled so that the policy is explicit and testable.
type Client struct {
	sdk openai.Client
	cfg Config
}

// New creates a client. It never performs I/O; the first request happens on
// Stream.
func New(cfg Config) *Client {
	cfg = cfg.withDefaults()
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Client{sdk: openai.NewClient(opts...), cfg: cfg}
}

// Config returns the normalized configuration.
func (c *Client) Config() Config { return c.cfg }

// Model returns the default model.
func (c *Client) Model() string { return c.cfg.Model }

// SetModel changes the default model (used by the REPL /model command).
func (c *Client) SetModel(model string) {
	if model != "" {
		c.cfg.Model = model
	}
}

// Stream performs a streaming chat completion and retries transient failures
// with exponential backoff. It returns the (possibly partial) result together
// with a classified *Error on failure.
func (c *Client) Stream(ctx context.Context, opts StreamOptions) (*Result, error) {
	if opts.Model == "" {
		opts.Model = c.cfg.Model
	}
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return &Result{Model: opts.Model, Attempts: 0, Elapsed: 0}, classify(err, 0, false)
	}

	maxAttempts := c.cfg.MaxRetries + 1
	var lastResult *Result
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := c.streamOnce(ctx, opts)
		if res == nil {
			res = &Result{Model: opts.Model}
		}
		res.Attempts = attempt
		res.Elapsed = time.Since(start)
		if err == nil {
			return res, nil
		}
		lastResult, lastErr = res, err

		// Never retry after the caller saw output, after cancellation, or on
		// non-transient errors.
		if ctx.Err() != nil || res.Started || attempt == maxAttempts {
			break
		}
		delay, retryable := retryDecision(err, attempt, c.cfg)
		if !retryable {
			break
		}
		reason, status := retryReason(err)
		if opts.OnRetry != nil {
			opts.OnRetry(RetryInfo{
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Delay:       delay,
				Reason:      reason,
				Status:      status,
			})
		}
		if serr := sleepCtx(ctx, delay); serr != nil {
			lastErr = serr
			break
		}
	}
	if lastResult == nil {
		lastResult = &Result{Model: opts.Model, Attempts: 1, Elapsed: time.Since(start)}
	}
	if lastErr == nil {
		lastErr = errors.New("request failed without an error")
	}
	return lastResult, classify(lastErr, lastResult.Attempts, lastResult.Started)
}

func (c *Client) streamOnce(ctx context.Context, opts StreamOptions) (*Result, error) {
	reqCtx := ctx
	if c.cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.cfg.RequestTimeout)
		defer cancel()
	}

	params := openai.ChatCompletionNewParams{
		Model:    opts.Model,
		Messages: opts.Messages,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}
	if len(opts.Tools) > 0 {
		params.Tools = opts.Tools
	}
	if opts.ResponseFormat != nil {
		params.ResponseFormat = *opts.ResponseFormat
	}
	if c.cfg.Temperature != nil {
		params.Temperature = param.NewOpt(*c.cfg.Temperature)
	}
	if c.cfg.MaxTokens > 0 {
		params.MaxTokens = param.NewOpt(int64(c.cfg.MaxTokens))
	}

	stream := c.sdk.Chat.Completions.NewStreaming(reqCtx, params)
	defer stream.Close()

	acc := NewAccumulator()
	started := false
	emit := func(d Delta) {
		if d.Text != "" {
			started = true
			if opts.OnText != nil {
				opts.OnText(d.Text)
			}
		}
		if d.Reasoning != "" {
			started = true
			if opts.OnReasoning != nil {
				opts.OnReasoning(d.Reasoning)
			}
		}
	}
	for stream.Next() {
		acc.Consume(stream.Current(), emit)
	}

	res := acc.Result()
	res.Started = started
	if err := stream.Err(); err != nil {
		return res, err
	}
	return res, nil
}
