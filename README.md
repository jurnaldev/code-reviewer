# gitlab-mr-review-bot

Automated GitLab merge-request reviewer powered by an LLM.

## Smoke test

```bash
cp config.example.yaml config.yaml
# fill GITLAB_TOKEN, ANTHROPIC_API_KEY in env
./review-cli --config config.yaml https://gl.your-corp.com/team/proj/-/merge_requests/N
```
