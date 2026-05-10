# code-reviewer

Automated GitLab merge-request reviewer powered by an LLM.

## Running the Discord Bot

1. Create a Discord application at https://discord.com/developers/applications
2. Add a bot user; copy the bot token to `DISCORD_TOKEN`.
3. Copy the application ID to `DISCORD_APP_ID`.
4. Invite the bot to your server with the `applications.commands` and `bot` scopes.
5. Fill in `config.yaml` (see `config.example.yaml`) and the env vars above plus `GITLAB_TOKEN` and your LLM API key.
6. Run:
   ```
   go build ./cmd/bot && ./bot --config config.yaml
   ```
7. In any channel the bot can see, run `/review url:<gitlab-mr-url>`.

The bot supports four providers via `llm.provider` in `config.yaml`:

- `anthropic` (default): set `ANTHROPIC_API_KEY`. Uses prompt caching.
- `openai`: set `OPENAI_API_KEY`. Uses Chat Completions with JSON-object response format.
- `ollama`: no API key needed; defaults to `http://localhost:11434`. Override with `llm.base_url`.
- `openrouter`: set `OPENROUTER_API_KEY` as `llm.api_key`. Defaults to `https://openrouter.ai/api`. Use OpenRouter slugs like `openai/gpt-4o` or `anthropic/claude-3.5-sonnet` for `llm.model`. Optionally set `llm.referer` and `llm.title` for app ranking. JSON mode is not sent (works across all OR models); `ParseFindings` tolerates prose-wrapped JSON.

## Memory (optional)

Set `memory.enabled: true` in `config.yaml` to give the bot persistent project memory across reviews. Three sources combine into one context block:

- **`.review/rules.md` in the MR target branch** — curated team rules; highest priority.
- **mem9** — durable LLM-extracted conventions and recent MR summaries (`X-API-Key` auth).
- **Local mirror** — `~/.cache/gitlab-mr-bot/memory/<project>.md`, two-way synced with mem9. Edit by hand to add or correct entries; sync on next review.

After every review, an extractor LLM pass distills durable conventions and a one-line MR summary, writes them to mem9 and the mirror.

The bot's final Discord reply has 👍 / 👎 buttons. Clicks record per-MR maintainer feedback; future reviews use down-rated signals to suppress repeat suggestion patterns.

All memory operations are best-effort — failures never block a review.

Set `MEM9_API_KEY` in env. See the `memory:` block in `config.example.yaml` for tuning.

## Smoke test

```bash
cp config.example.yaml config.yaml
# fill GITLAB_TOKEN, ANTHROPIC_API_KEY in env
./review-cli --config config.yaml https://gl.your-corp.com/team/proj/-/merge_requests/N
```
