# Memory layer for GitLab MR Reviewer Bot (mem9 + markdown)

**Date:** 2026-05-10
**Status:** Design — pending implementation plan
**Owner:** fahmi

## Goal

Give the GitLab MR reviewer bot persistent memory so reviews improve over time. Memory feeds the LLM with three streams of project-scoped context:

1. **Project conventions** learned across past reviews.
2. **Cross-MR context** (recent merged-MR summaries) for the same project.
3. **Maintainer feedback** (👍 / 👎 on bot reviews) used to suppress repeat suggestions of disliked kinds.

Backed by [mem9](https://github.com/mem9-ai/mem9) for durable vector+FTS recall, layered with two markdown sources: a curated repo-side rules file and a local two-way-synced mirror.

## Non-goals (v1)

- Per-author memory or personalization beyond project scope.
- Reading repo docs (README/CONTRIBUTING/ARCHITECTURE) as context — deferred to v2.
- GitLab discussion-thread polling for in-thread feedback — deferred to v2.
- Per-finding feedback granularity. v1 captures one 👍/👎 per MR review.

## Design overview

A new `internal/memory` package mediates between the orchestrator and three context sources/sinks. Recall composes context once per MR; writes happen post-review (extractor) and on Discord button clicks (feedback). All paths are best-effort — memory failures never block a review.

```
            ┌───────────────────────────────────────────────────────┐
            │                  review.Orchestrator                  │
            └────────────┬────────────────────────────┬─────────────┘
                         │ Recall(ctx, project, mr)   │ ExtractAndWrite(...)
                         ▼                            ▼
            ┌───────────────────────────────────────────────────────┐
            │              memory.Composite (Client)                │
            └─┬───────────────────┬────────────────┬────────────────┘
              ▼                   ▼                ▼
       reporules (read-only)   mem9 (RW)      mirror (RW)
       GitLab raw file API     mem9 REST      stamped local .md
```

`internal/discord/bot.go` separately writes feedback memories via `memory.Client.WriteFeedback(...)` when buttons are clicked.

## Sources & schema

### Source priority (recall order, highest first)

1. **Repo rules** — verbatim block from `.review/rules.md` on MR target branch. Curated by humans, highest trust.
2. **mem9 conventions** — durable LLM-extracted rules.
3. **Mirror conventions** — local file entries (may include human-added not yet in mem9).
4. **mem9 MR summaries** — recent project MRs, time-bounded.
5. **mem9 feedback signals** (down-rated only, brief) — used by extractor, not in recall block.

When `recall_token_budget` is exceeded, drop lowest-priority section first; final block notes truncation.

### mem9 record shape

Single shared mem9 space. Every record tagged with `project:<gitlab-project-path>`.

| Type | content | tags | metadata |
|---|---|---|---|
| convention | `Prefer X over Y because Z` (one rule per record) | `project:X`, `type:convention`, optional `category:bug\|security\|perf\|test\|style` | `derived_from_mr_iid`, `derived_at` |
| mr-summary | `!<iid> "<title>" — <1-2 sentence summary>` | `project:X`, `type:mr-summary` | `mr_iid`, `mr_title`, `head_sha`, `at` |
| feedback | `MR !<iid> review rated <up\|down> by <user>` | `project:X`, `type:feedback`, `rating:up\|down` | `mr_iid`, `rated_by`, `at` |

Auth: `X-API-Key` header on all v1alpha2 calls.

### Repo rules file

- Path: `.review/rules.md` in MR target branch (configurable).
- Fetched at review time via new `gitlab.Client.GetFileRaw(ctx, project, path, ref)` wrapping `GET /projects/:id/repository/files/:path/raw?ref=:branch`.
- 404 → skip silently (most repos won't have one).
- Content size cap: 4 KB; if larger, keep first 4 KB and append `_(truncated)_` marker.

### Mirror file

- Location: `~/.cache/gitlab-mr-bot/memory/<url-safe-project-path>.md`.
- Format:

```markdown
# Memory: group/repo

## Conventions
- Prefer errors.Is over == for sentinel errors. <!-- mem9_id: m_abc123 -->
- Wrap external HTTP behind retry helper. <!-- mem9_id: m_def456 -->
- New entry not yet pushed to mem9.

## Recent MRs
- !123 "fix nil deref" — summary. <!-- mem9_id: m_ghi789 -->

## Recent Feedback
- !123 rated 👎 by @alice on 2026-05-10
```

- Each entry stamped with `<!-- mem9_id: <id> -->` after write to mem9.
- Bullets without a stamp are treated as human-added pending-push.

## Recall flow

Called once per MR by orchestrator before chunk loop.

1. `composite.Recall(ctx, projectPath, mr)` fires three concurrent reads:
   - Repo rules: `gitlab.Client.GetFileRaw(...)`.
   - mem9: two parallel queries:
     - `GET /v1alpha2/mem9s/memories?q=<built>&tags=project:X,type:convention&limit=20&mode=hybrid`
     - `GET /v1alpha2/mem9s/memories?q=<built>&tags=project:X,type:mr-summary&limit=5&mode=hybrid`
     - Query string built from MR title + changed file paths, capped 200 chars.
   - Mirror: read `<dir>/<slug>.md`, parse bullets into `Convention`/`MRSummary` structs by section.
2. **Mirror→mem9 sync** (runs synchronously inside Recall, before merge):
   - Diff mirror entries vs. mem9 entries by stamp.
   - Push novel/edited mirror entries. Update local file with new stamps.
3. **Merge**: dedup conventions across mem9+mirror by `mem9_id`. Mirror entries without stamp included as-is.
4. **Render** to one markdown block via `format.go` with priority-aware token cap.
5. Returned block stored on orchestrator, written into `ReviewRequest.FileContext` for every chunk so Anthropic prompt cache stays warm. Implementation note: each provider must place `FileContext` in the cached prefix region (after system prompt, before per-chunk diff) — verify in `internal/llm/anthropic.go` cache-control breakpoints.

Soft fail: any source error → that source contributes empty content, log, continue.

### Sync rules table

| Local state | mem9 state | Action |
|---|---|---|
| has stamp m_X, mem9 has m_X, content same | — | no-op |
| has stamp m_X, mem9 has m_X, local content differs, local mtime newer | — | `PUT /v1alpha2/mem9s/memories/m_X` |
| no stamp, content novel | not in mem9 | `POST` → write stamp back to file |
| has stamp m_X | mem9 missing | leave local (someone deleted upstream) |
| no local entry | mem9 has m_Y | append to file with stamp |

Mirror file mtime + content hash drives novelty/edit detection.

## Write flow (post-review extraction)

Called by orchestrator after `PostNote` succeeds.

1. Build extractor user message: MR title, description, file list, aggregated findings (severity+category+message), recent down-rated feedback memories for project (up to 5).
2. `llm.Provider.Generate(ctx, extractorSystemPrompt, userMsg)` — new method on `Provider` interface, plain-text response, expected JSON.
3. Parse `{summary: string, conventions: []string}`. Drop on parse error.
4. For each convention: `POST /v1alpha2/mem9s/memories` (mem9) + append stamped bullet to mirror file.
5. Summary: same.
6. All best-effort, errors logged not returned.

### Extractor system prompt (`extractor_prompt.md`)

> From this MR's title, description, file list, findings, and recent down-rated feedback for the project, identify durable conventions worth remembering for future reviews. Skip MR-specific facts. Skip findings the maintainer rejected (down-rated). Output strict JSON: `{"summary": "<1-2 sentence MR summary>", "conventions": ["<one rule per string>", ...]}`. Each convention: one sentence, generally applicable, project-relevant. Empty arrays fine.

## Feedback flow

1. Bot's final Discord message (`editFinal`) carries two button components:
   - 👍 Success-style, `custom_id = review_feedback:up:<jobID>`
   - 👎 Danger-style, `custom_id = review_feedback:down:<jobID>`
2. `Bot.HandleInteraction` adds `case discordgo.InteractionMessageComponent`:
   - Parse `custom_id`.
   - Fetch job from `jobs.Tracker` (extended retention 24 h).
   - Resolve `projectPath` + `mr_iid` via `gitlab.ParseURL(job.MRURL)`.
   - Call `memory.Client.WriteFeedback(ctx, projectPath, mrIID, rating, userID)`.
   - Reply ephemeral "noted, thanks".
3. Buttons remain on the message for repeat clicks; each click writes a new feedback record (rating may flip).
4. Mirror file gets bullet appended under `## Recent Feedback` for human visibility. No two-way sync on feedback section (read-only by humans).

## Configuration

```yaml
memory:
  enabled: true
  recall_token_budget: 2000
  http_timeout: 10s

  mem9:
    enabled: true
    base_url: https://api.mem9.ai      # or http://localhost:8080
    api_key: env:MEM9_API_KEY
    conventions_top_k: 20
    summaries_top_k: 5

  repo_rules:
    enabled: true
    path: .review/rules.md             # in MR target branch

  mirror:
    enabled: true
    dir: ~/.cache/gitlab-mr-bot/memory # one .md per project
```

- `memory.enabled=false` (or block missing) → `noop.Client` wired. Existing deployments unaffected.
- Each per-source `enabled: false` skips that source independently.
- `dir` expands `~` via `os.UserHomeDir()`. Created on first write (`MkdirAll 0o755`).
- `validate(c)` rejects mem9 enabled with empty `api_key`.

## Module map

```
internal/memory/
  client.go                 — Memory interface (Recall, Write, WriteFeedback)
  composite.go              — Recall fans out to all enabled Sources, merges, syncs mirror
  format.go                 — render []Memory → FileContext block, priority-aware cap
  types.go                  — Memory, Convention, MRSummary, Feedback structs
  noop.go                   — disabled-mode impl
  extractor.go              — second-LLM extraction
  extractor_prompt.md
  mem9/
    client.go               — REST client (POST/GET/PUT memories, search)
    client_test.go
  reporules/
    source.go               — read-only fetch via gitlab.Client.GetFileRaw
    source_test.go
  mirror/
    file.go                 — parse + render stamped .md
    sync.go                 — 3-way merge logic
    file_test.go
    sync_test.go
internal/gitlab/client.go   — add GetFileRaw(ctx, project, path, ref) (string, error)
internal/llm/provider.go    — add Generate(ctx, sys, user) (string, TokenUsage, error)
internal/llm/anthropic.go   — implement Generate
internal/llm/openai.go      — implement Generate
internal/llm/ollama.go      — implement Generate
internal/llm/openrouter.go  — implement Generate
internal/review/orchestrator.go — wire memory.Recall + ExtractAndWrite
internal/discord/bot.go     — add buttons + InteractionMessageComponent handler
internal/jobs/jobs.go       — add 24h retention sweep for completed jobs
internal/config/config.go   — Memory block + sub-blocks + validation
cmd/bot/main.go             — construct memory client, pass to orchestrator + bot
config.example.yaml         — document memory block
```

## Error handling

| Failure | Behavior |
|---|---|
| mem9 5xx/timeout on recall | log, treat source as empty |
| mem9 5xx/timeout on write | log, mirror still written |
| repo rules file 404 | silent skip |
| repo rules file 5xx | log, skip |
| mirror dir missing | create on first write |
| mirror file parse error | log, treat as empty, do not delete |
| mirror sync conflict (same stamp, both edited) | mem9 wins; record local diff in log |
| extractor LLM error | log, skip writes |
| extractor JSON parse error | log, skip writes |
| Discord button click after job evicted | ephemeral reply "review expired", no write |

mem9 client uses existing `internal/httpretry` for backoff on 5xx/429. All recall/write paths bounded by `memory.http_timeout`.

## Testing

| Layer | Approach |
|---|---|
| `mem9.client` | `httptest.Server` covering POST/GET/PUT, error mapping, header propagation |
| `reporules.source` | stub `gitlab.Client` for 200/404/5xx, content-size cap |
| `mirror.file` | golden tests for parse + render including stamps and human-edited bullets |
| `mirror.sync` | table tests covering each row in sync rules table |
| `composite` | golden tests for token-cap truncation across priority sections |
| `extractor` | stub `llm.Provider.Generate`, JSON happy/malformed paths |
| `noop` | contract test: all methods no-op, never error |
| `memory.client` integration | all sources stubbed, assert merged FileContext + write fan-out |
| `review.orchestrator` e2e | extend existing `e2e_test.go` with stub mem9 server; assert orchestrator soft-fails when server returns 500 |
| `discord.bot` | extend `bot_test.go` for `InteractionMessageComponent` with mock memory client |

## Rollout

1. Land `internal/memory` package with all sources wired but `memory.enabled=false` default — no behavior change.
2. Manual smoke: enable on one repo with self-hosted mem9, run /review, inspect mirror file.
3. Flip default once stable.

## Open follow-ups (not v1)

- Repo docs (README/CONTRIBUTING/ARCHITECTURE) auto-summarized into context.
- Per-finding feedback via Discord buttons on per-finding posts.
- GitLab discussion-thread polling for emoji/keyword acceptance.
- Per-author memory.
- TTL/decay on MR summaries to keep recall fresh.
