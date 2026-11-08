package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
)

// ErrorKind is a coarse classification used by the CLI to print an actionable
// hint instead of a raw SDK error dump.
type ErrorKind string

const (
	KindCanceled   ErrorKind = "canceled"
	KindTimeout    ErrorKind = "timeout"
	KindAuth       ErrorKind = "auth"
	KindPayment    ErrorKind = "payment"
	KindRateLimit  ErrorKind = "rate_limit"
	KindServer     ErrorKind = "server"
	KindNetwork    ErrorKind = "network"
	KindBadRequest ErrorKind = "bad_request"
	KindNotFound   ErrorKind = "not_found"
	KindStream     ErrorKind = "stream"
	KindUnknown    ErrorKind = "unknown"
)

// Error wraps the underlying transport or API error with retry metadata and a
// user-facing classification.
type Error struct {
	Kind     ErrorKind
	Status   int
	Attempts int
	Err      error
	// Partial is true when the failure happened after some output had already
	// been streamed to the terminal.
	Partial bool
}

func (e *Error) Error() string {
	detail := describe(e.Err)
	if e.Attempts > 1 {
		return fmt.Sprintf("%s: %s (after %d attempts)", e.Kind, detail, e.Attempts)
	}
	return fmt.Sprintf("%s: %s", e.Kind, detail)
}

// Unwrap lets errors.Is/As inspect the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// Summary is a short Chinese label for the CLI.
func (e *Error) Summary() string {
	switch e.Kind {
	case KindCanceled:
		return "请求已取消"
	case KindTimeout:
		return "请求超时"
	case KindAuth:
		return "认证失败"
	case KindPayment:
		return "账户余额不足"
	case KindRateLimit:
		return "触发服务端限流"
	case KindServer:
		return "服务端错误"
	case KindNetwork:
		return "网络连接失败"
	case KindBadRequest:
		return "请求被服务端拒绝"
	case KindNotFound:
		return "接口或模型不存在"
	case KindStream:
		return "流式响应中断"
	default:
		return "调用失败"
	}
}

// Hint returns a concrete next step for the user. Empty means no hint.
func (e *Error) Hint() string {
	host := endpointOf(e.Err)
	if host == "" {
		host = "API"
	}
	switch e.Kind {
	case KindCanceled:
		return "已按 Ctrl+C 取消当前请求；会话历史保留，可以继续提问。"
	case KindTimeout:
		return fmt.Sprintf("单个请求超过超时时间。可用 --timeout 300s 放宽；若网络不稳定，确认代理是否生效（当前目标 %s）。", host)
	case KindAuth:
		return "检查 API Key：设置 DEEPSEEK_API_KEY 或 OPENAI_API_KEY，并确认 --base-url 与 Key 的来源匹配。"
	case KindPayment:
		return "服务端返回 402：账户余额不足，请充值后重试。"
	case KindRateLimit:
		if e.Attempts > 1 {
			return fmt.Sprintf("已自动退避重试 %d 次仍未成功。降低请求频率，或稍后再试。", e.Attempts-1)
		}
		return "降低请求频率，或稍后再试。"
	case KindServer:
		return fmt.Sprintf("服务端连续返回 5xx（共尝试 %d 次）。稍后重试；若持续出现，查看服务商状态页。", e.Attempts)
	case KindNetwork:
		return fmt.Sprintf("无法连接 %s：网络不可用、代理未生效或 Base URL 错误。可先运行本地 mock 验证客户端：go run ./cmd/mockllm（另一个终端），再执行 agent --base-url http://127.0.0.1:18080/v1。", host)
	case KindBadRequest:
		return "常见原因：模型名不存在、参数不被支持（DeepSeek 不支持 response_format=json_schema，请用 --json-mode=object）、上下文超长。"
	case KindNotFound:
		return "检查 --base-url（DeepSeek 为 https://api.deepseek.com）以及 --model 是否可用。"
	case KindStream:
		if e.Partial {
			return "已输出部分内容，为避免重复不会自动重试。可重新提问或说「继续」。"
		}
		return "连接在响应中途断开。检查网络/代理后重试。"
	default:
		return "加 --verbose 查看完整错误。"
	}
}

func classify(err error, attempts int, partial bool) *Error {
	e := &Error{Attempts: attempts, Err: err, Partial: partial}
	if err == nil {
		e.Kind = KindUnknown
		return e
	}
	if errors.Is(err, context.Canceled) {
		e.Kind = KindCanceled
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		e.Kind = KindTimeout
		return e
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		e.Status = apiErr.StatusCode
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized, apiErr.StatusCode == http.StatusForbidden:
			e.Kind = KindAuth
		case apiErr.StatusCode == http.StatusPaymentRequired:
			e.Kind = KindPayment
		case apiErr.StatusCode == http.StatusRequestTimeout,
			apiErr.StatusCode == http.StatusConflict,
			apiErr.StatusCode == http.StatusTooEarly,
			apiErr.StatusCode == http.StatusTooManyRequests:
			e.Kind = KindRateLimit
		case apiErr.StatusCode == http.StatusNotFound:
			e.Kind = KindNotFound
		case apiErr.StatusCode >= 400 && apiErr.StatusCode < 500:
			e.Kind = KindBadRequest
		case apiErr.StatusCode >= 500:
			e.Kind = KindServer
		default:
			e.Kind = KindUnknown
		}
		return e
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected EOF") ||
		strings.Contains(err.Error(), "invalid character") ||
		strings.Contains(err.Error(), "INVALID_JSON") {
		e.Kind = KindStream
		return e
	}
	if isNetworkErr(err) {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			e.Kind = KindTimeout
		} else {
			e.Kind = KindNetwork
		}
		return e
	}
	e.Kind = KindUnknown
	return e
}

func describe(err error) string {
	if err == nil {
		return "unknown error"
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		msg := strings.TrimSpace(apiErr.Message)
		if msg == "" {
			msg = strings.TrimSpace(apiErr.RawJSON())
		}
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return fmt.Sprintf("HTTP %d %s: %s", apiErr.StatusCode, http.StatusText(apiErr.StatusCode), msg)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		inner := urlErr.Err
		if inner != nil {
			return inner.Error()
		}
	}
	return err.Error()
}

func endpointOf(err error) string {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.Request != nil && apiErr.Request.URL != nil {
		return apiErr.Request.URL.Host
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, err := url.Parse(urlErr.URL); err == nil {
			return u.Host
		}
	}
	return ""
}

func isNetworkErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection refused", "connection reset", "no such host",
		"network is unreachable", "tls handshake", "i/o timeout",
		"server misbehaving",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// retryDecision reports whether err is transient and how long to wait. It
// intentionally does not retry context cancellation, caller deadlines, 4xx
// client errors, or a stream that already produced visible output.
func retryDecision(err error, attempt int, cfg Config) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
			return cfg.backoff(attempt, retryAfter(apiErr)), true
		}
		if apiErr.StatusCode >= 500 && apiErr.StatusCode <= 599 {
			return cfg.backoff(attempt, 0), true
		}
		return 0, false
	}
	if isNetworkErr(err) {
		return cfg.backoff(attempt, 0), true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return cfg.backoff(attempt, 0), true
	}
	return 0, false
}

func retryAfter(e *openai.Error) time.Duration {
	if e.Response == nil {
		return 0
	}
	v := e.Response.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func (c Config) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > 60*time.Second {
			return 60 * time.Second
		}
		return retryAfter
	}
	d := c.BackoffBase << (attempt - 1)
	if d > c.BackoffMax {
		d = c.BackoffMax
	}
	if d <= 0 {
		return time.Second
	}
	// Full-ish jitter: 50%-100% of the computed delay, so a burst of clients
	// does not retry in lockstep.
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func retryReason(err error) (string, int) {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("HTTP %d", apiErr.StatusCode), apiErr.StatusCode
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", 0
	}
	if isNetworkErr(err) {
		return "network error", 0
	}
	return "transient error", 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
