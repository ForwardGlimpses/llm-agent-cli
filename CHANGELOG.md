# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).
Before `v1.0.0`, a `MINOR` release may contain breaking changes; they are
marked **Breaking** below.

## [Unreleased]

### Planned

- Config file and model profiles (`v0.2`)
- Context budget and history compaction (`v0.3`)
- Parallel tool calls and tool approval (`v0.4`)

## [0.1.0] - 2026-11-08

Initial public baseline.

### Added

- OpenAI-compatible chat completions over the official `openai-go` SDK,
  with DeepSeek defaults.
- SSE streaming with incremental text output and a CLI renderer that keeps
  answers on stdout and diagnostics on stderr.
- Tool-calling loop: `tool_calls` fragments are assembled by `index`, tools are
  executed locally, and results are fed back as `tool` messages.
- Built-in tools: `get_current_time`, `calculator` (safe recursive-descent
  parser), and `read_file` restricted to a working-directory root.
- Structured output: JSON Schema validation, extraction from Markdown fences,
  and repair rounds that feed `$.path` validation errors back to the model.
- Retry policy with exponential backoff and jitter, `Retry-After` support, and
  a hard rule against retrying after visible output or local timeouts.
- Layered timeouts and cancellation: turn, request, tool, and Ctrl+C handling.
- Token accounting and cost estimation, including cached input tokens and
  DeepSeek's `prompt_cache_hit_tokens`.
- CLI modes: one-shot question, interactive REPL, piped stdin, and flags before
  or after the question.
- Local `mockllm` OpenAI-compatible SSE server for offline demos and tests.
- Architecture diagrams and release/development documentation.

### Notes

- Default prices are estimates in USD per 1M tokens and may be stale; override
  them with `--price-in`, `--price-cached`, and `--price-out`.
