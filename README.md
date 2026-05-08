# gitlab-mr-review-bot

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

## Smoke test

```bash
cp config.example.yaml config.yaml
# fill GITLAB_TOKEN, ANTHROPIC_API_KEY in env
./review-cli --config config.yaml https://gl.your-corp.com/team/proj/-/merge_requests/N
```
