# GitLab MR Review Bot — Design

**Date:** 2026-05-08
**Status:** Approved (brainstorming phase)

## Summary

A Discord bot, written in Go, that reviews GitLab merge requests on demand. A user invokes `/review <mr-url>` in Discord; the bot fetches the MR diff from a self-hosted GitLab instance, runs an AI code review through a configurable LLM provider, and posts the findings back to the GitLab MR as a summary note plus inline discussion threads. Discord receives status updates and a final link.

## Goals

- Manual, on-demand AI review of GitLab MRs from Discord
- Broad review scope: bugs, security, performance, test coverage, style
- Output posted to GitLab as a summary note + inline discussions
- Self-hosted GitLab support (configurable base URL)
- Provider-agnostic LLM layer (Anthropic, OpenAI, Ollama; pluggable)
- Robust handling of large MRs without runaway cost

## Non-Goals (v1)

- Webhook-based auto-review on MR open/update
- Persistence across restarts (in-memory job state is acceptable)
- Multi-tenant SaaS deployment
- Code-fix auto-application via push
- Slack support, web UI, CLI front-end (single Discord interface)

## Architecture

Single Go binary, single process. No external queue, no database in v1.

### Packages

```
cmd/bot/                 main, config load, dependency wiring
internal/discord/        slash command handler, status updates
internal/gitlab/         GitLab REST client (MR fetch, post comment, post discussion)
internal/llm/            provider-agnostic interface + adapters (anthropic, openai, ollama)
internal/review/         orchestrator: filter → chunk → prompt → parse → aggregate
internal/diff/           unified diff parser, hunk splitter, file classifier
internal/jobs/           in-memory job tracker (id → status, progress, result)
internal/config/         env/yaml config: tokens, gitlab URL, model, limits
```

Each `internal/*` package exposes a small interface and is consumed by `internal/review` (the orchestrator) or `cmd/bot` (wiring). Adapters in `internal/llm` and `internal/gitlab` make external calls; everything else is pure logic and easy to test.

### Request flow

1. User runs `/review <mr-url>` (optionally `--deep`) in Discord.
2. Bot defers the interaction (Discord 3s ack), creates a job, replies with job ID and "queued" status.
3. Orchestrator fetches MR metadata + changes via GitLab API.
4. **Filter** step drops lockfiles, generated, vendored, binary, minified files (configurable globs).
5. **Chunk** step produces one diff chunk per file; oversized files split by hunk groups within token budget.
6. **LLM** step issues parallel calls (bounded concurrency), with a cached system prompt containing the review rubric. Each call returns structured JSON findings.
7. **Aggregate** step deduplicates findings, sorts by severity, and builds a summary body.
8. **Post** step writes one summary note to the MR, then one inline discussion per finding that has a valid line position; findings whose line cannot be anchored fall through to the summary.
9. Discord interaction is edited with a final status: counts of findings by severity + a link to the MR.

## Components and Interfaces

### LLM provider

```go
type Provider interface {
    Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
    Name() string
}

type ReviewRequest struct {
    SystemPrompt string  // cacheable across chunks of the same job
    FilePath     string
    DiffChunk    string
    FileContext  string  // optional surrounding code, future
}

type ReviewResponse struct {
    Findings []Finding
    Usage    TokenUsage
}

type Finding struct {
    Severity   string  // "critical" | "major" | "minor" | "nit"
    Category   string  // "bug" | "security" | "perf" | "test" | "style"
    File       string
    Line       int     // line number in the new file
    Message    string
    Suggestion string  // optional code-fix snippet
}
```

The Anthropic adapter sets `cache_control: ephemeral` on the system block so repeated chunks within a job benefit from prompt caching. Other adapters pass through without caching.

The model is instructed (in the system prompt) to emit findings as a JSON object: `{"findings":[{...}]}`. The parser tolerates trailing prose by extracting the first balanced JSON block.

### GitLab client

```go
type Client interface {
    GetMR(projectID, mrIID int) (*MR, error)
    GetMRChanges(projectID, mrIID int) ([]FileChange, error)
    PostNote(projectID, mrIID int, body string) error
    PostDiscussion(projectID, mrIID int, body string, pos Position) error
}

type Position struct {
    BaseSHA, StartSHA, HeadSHA string
    NewPath, OldPath           string
    NewLine                    int
}
```

The bot uses a personal access token with `api` scope. Project ID and MR IID are parsed from the supplied URL.

### Job tracker

```go
type Job struct {
    ID         string
    MRURL      string
    Status     string  // queued | fetching | reviewing | posting | done | error
    Progress   string  // human-readable, e.g. "3/12 files reviewed"
    StartedAt  time.Time
    Findings   int
    ErrMessage string
}
```

Backed by `map[string]*Job` guarded by `sync.RWMutex`. A ticker (~5s) edits the Discord interaction with the latest job status. Jobs are cleaned up 24h after completion.

### Configuration

YAML file with environment variable interpolation. Single example:

```yaml
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
  allowed_user_ids: []        # optional allowlist
  allowed_role_ids: []
gitlab:
  base_url: https://gitlab.company.com
  token: env:GITLAB_TOKEN
llm:
  provider: anthropic         # anthropic | openai | ollama
  model: claude-sonnet-4-6
  api_key: env:ANTHROPIC_API_KEY
  base_url: ""                # for ollama or self-hosted gateways
review:
  max_file_tokens: 8000
  max_mr_tokens: 200000
  max_concurrent_chunks: 4
  job_timeout: 10m
  llm_call_timeout: 90s
  ignore_globs:
    - "**/*.lock"
    - "vendor/**"
    - "node_modules/**"
    - "**/*.gen.*"
    - "**/*.min.*"
  deep_mode_default: false
```

## Large MR Handling

Filters (drop before any LLM call):
- Lockfiles: `package-lock.json`, `yarn.lock`, `go.sum`, `Cargo.lock`, `poetry.lock`, `*.lock`
- Generated: `vendor/`, `node_modules/`, `dist/`, `build/`, `*.pb.go`, `*.gen.*`, `*_generated.*`
- Binary or minified: `*.min.js`, images, PDFs, files marked binary in the diff
- All extensible via `review.ignore_globs`

Chunking:
- One LLM call per file by default
- Files exceeding `max_file_tokens` are split into hunk groups within budget
- A single hunk that exceeds the budget alone is sent truncated, and a finding records "file too large for full review"

Concurrency and budget:
- LLM calls bounded by `max_concurrent_chunks`
- Total per-job token cap `max_mr_tokens`; on exceedance, remaining files are skipped and the summary lists what was dropped
- `job_timeout` cancels the orchestrator context; partial results are still posted

Prompt caching:
- The system prompt (review rubric) is marked cacheable; the Anthropic adapter sets `cache_control: ephemeral`. Other providers ignore.

Two-pass deep mode (off by default):
- Pass 1 produces per-file findings
- Pass 2 sends all findings to one aggregator call for global dedupe and a higher-quality summary
- Enabled by appending `--deep` to the slash command

## Error Handling

**Discord layer**
- Invalid MR URL → ephemeral reply with expected URL shape
- User not allowlisted → ephemeral "not authorized"
- Duplicate job for the same MR → reply with the existing job ID
- 3-second ack limit handled with `defer` + later edit

**GitLab layer**
- 401/403 → fail job, Discord reply naming the likely cause (token scope)
- 404 → "MR not found or no access"
- 5xx / network → 3 retries with exponential backoff, then fail
- 429 → respect `Retry-After`, retry once, else fail
- Empty diff (renames only) → skip and post "no reviewable changes"

**LLM layer**
- 429 / overloaded → exponential backoff, max 3 retries per chunk
- Per-call timeout `llm_call_timeout` → mark chunk failed, other chunks continue
- Malformed JSON → one re-prompt attempting "return valid JSON only"; else drop the chunk's findings and log
- Partial failures → post whatever succeeded; summary states "N of M files reviewed (X failed)"
- Token cap exceeded mid-job → stop dispatch, finalize with what is in hand

**Posting layer**
- Inline discussion fails (line not in diff range) → fall back to listing the finding under the summary
- Summary post fails → 2 retries; on final failure, Discord reply contains the full review text so the user can paste manually

**Job lifecycle**
- Bot crash mid-review → in-flight jobs are lost (acceptable for v1); README documents re-running `/review`
- Stale jobs swept hourly with a 24h TTL after completion

**Logging**
- Structured logs via `slog`, per-job ID
- Diff content and LLM responses are not logged at info level; debug level only, opt-in
- Token usage and cost tracked per job and surfaced in the Discord status

## Testing Strategy

**Unit (std `testing`, table-driven)**
- `internal/diff` — parser fixtures, hunk splitter, file classifier (lockfile / binary / generated detection)
- `internal/review/chunker` — token budgeting, hunk grouping
- `internal/review/parser` — LLM JSON output parser (valid, trailing prose, malformed, partial)
- `internal/review/aggregator` — dedupe, severity sort, summary body building
- `internal/config` — env override, defaults, validation

**Integration (mocked transports)**
- `internal/gitlab` driven against `httptest.Server`: covers fetch, post note, post discussion, 401, 404, 429, 5xx
- `internal/llm` adapters: mock HTTP, assert request shape (cache header for Anthropic), parse responses
- `internal/discord` with a mocked session: ack timing, status edit cadence

**End-to-end (one happy-path test)**
- httptest GitLab + fake LLM (canned findings)
- Drive `review.Run(ctx, mrURL)` directly, bypassing Discord
- Assert correct discussions posted, summary body matches snapshot, job ends `done`

**Manual smoke (documented in README)**
- Run against a staging MR with a real LLM and verify inline placement

**Tools and targets**
- `testify/require` (used sparingly), `go-snaps` for prompt and summary snapshots
- Coverage target: 70%+ in `internal/review` and `internal/diff`; adapters lower

## Open Questions / Future Work

- Webhook auto-trigger (deferred to v2)
- Multi-server (multiple Discord guilds) configuration
- Persistence layer (BoltDB, SQLite) for job recovery across restarts
- Cost ceiling per user / per day enforcement
- Per-repository custom rubrics (e.g., language-specific guidance)
- Reaction-driven actions (✅ to acknowledge a finding)
