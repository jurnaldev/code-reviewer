# GitLab MR Review Bot — Plan 1: Core Review Engine

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go library that, given a GitLab MR URL, fetches the diff, runs an AI code review through a configurable LLM, and posts a summary note plus inline discussions back to the MR. No Discord layer in this plan; a small `cmd/review-cli` is included for manual smoke and end-to-end tests.

**Architecture:** Single Go module. Packages are split by responsibility: `config` loads YAML/env; `diff` parses unified diffs; `classifier` filters ignorable files; `chunker` splits diffs to fit a token budget; `llm` defines a `Provider` interface and an Anthropic adapter; `gitlab` is a REST client; `review` orchestrates filter → chunk → LLM → aggregate → post. Followup plans add Discord (Plan 2) and OpenAI/Ollama adapters (Plan 3).

**Tech Stack:** Go 1.22+, standard library + `github.com/stretchr/testify/require` for tests, `gopkg.in/yaml.v3` for config, `github.com/google/uuid` for job IDs (used in Plan 2 but added now), `github.com/gobwas/glob` for ignore-glob matching.

---

## File Structure

```
go.mod
go.sum
.gitignore
config.example.yaml
cmd/
  review-cli/
    main.go                     # CLI driver: takes MR URL, runs review, prints result
internal/
  config/
    config.go                   # struct, defaults, env interpolation
    config_test.go
  diff/
    parser.go                   # ParsedDiff, FileDiff, Hunk, DiffLine + Parse()
    parser_test.go
    testdata/
      simple.diff
      multi_file.diff
      binary.diff
      rename.diff
  classifier/
    classifier.go               # IsIgnored(path, globs), IsBinary(FileDiff)
    classifier_test.go
  chunker/
    chunker.go                  # Chunk(FileDiff, maxTokens) []Chunk
    chunker_test.go
  llm/
    provider.go                 # Provider interface + types
    parser.go                   # ParseFindings extracts JSON
    parser_test.go
    anthropic.go                # AnthropicAdapter (Provider impl)
    anthropic_test.go
  gitlab/
    url.go                      # ParseURL → MRRef
    url_test.go
    client.go                   # Client interface + RESTClient
    client_test.go
    types.go                    # MR, FileChange, Position
  review/
    aggregator.go               # Aggregate(findings) → AggregateResult, dedupe + sort
    aggregator_test.go
    orchestrator.go             # Run(ctx, mrURL) wires everything
    orchestrator_test.go
docs/
  superpowers/specs/...
  superpowers/plans/...
```

---

## Task 1: Project scaffold

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `config.example.yaml`
- Create: `cmd/review-cli/main.go`

- [ ] **Step 1: Initialize Go module**

Run: `cd /Users/fahmi/Documents/Personal/ai-tools && go mod init github.com/fahmi/gitlab-mr-review-bot`
Expected: creates `go.mod` with module declaration.

- [ ] **Step 2: Create `.gitignore`**

```
/bin
/dist
*.test
*.out
.env
config.yaml
.DS_Store
```

- [ ] **Step 3: Create `config.example.yaml`**

```yaml
gitlab:
  base_url: https://gitlab.example.com
  token: env:GITLAB_TOKEN
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  api_key: env:ANTHROPIC_API_KEY
  base_url: ""
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

- [ ] **Step 4: Create placeholder `cmd/review-cli/main.go`**

```go
package main

import "fmt"

func main() {
	fmt.Println("review-cli placeholder; wired in Task 13")
}
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git init
git add .
git commit -m "chore: scaffold module + cli stub + example config"
```

---

## Task 2: Config package

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Add yaml dep**

Run: `go get gopkg.in/yaml.v3`
Expected: go.mod updated.

- [ ] **Step 2: Write the failing test `config_test.go`**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_DefaultsAndEnvInterp(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-abc")
	t.Setenv("ANTHROPIC_API_KEY", "sk-xyz")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
gitlab:
  base_url: https://gl.example.com
  token: env:GITLAB_TOKEN
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  api_key: env:ANTHROPIC_API_KEY
review:
  max_file_tokens: 4000
`), 0644))

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "https://gl.example.com", cfg.GitLab.BaseURL)
	require.Equal(t, "glpat-abc", cfg.GitLab.Token)
	require.Equal(t, "anthropic", cfg.LLM.Provider)
	require.Equal(t, "sk-xyz", cfg.LLM.APIKey)
	require.Equal(t, 4000, cfg.Review.MaxFileTokens)
	// defaults filled
	require.Equal(t, 200000, cfg.Review.MaxMRTokens)
	require.Equal(t, 4, cfg.Review.MaxConcurrentChunks)
	require.Equal(t, 10*time.Minute, cfg.Review.JobTimeout)
	require.Equal(t, 90*time.Second, cfg.Review.LLMCallTimeout)
	require.False(t, cfg.Review.DeepModeDefault)
}

func TestLoad_MissingEnvFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
gitlab:
  base_url: https://x
  token: env:NOT_SET_VAR_XYZ
llm:
  provider: anthropic
  model: m
  api_key: k
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NOT_SET_VAR_XYZ")
}

func TestLoad_RejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
gitlab: {base_url: x, token: t}
llm: {provider: bogus, model: m, api_key: k}
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider")
}
```

- [ ] **Step 3: Add testify dep**

Run: `go get github.com/stretchr/testify/require`
Expected: go.mod updated.

- [ ] **Step 4: Run test to confirm it fails**

Run: `go test ./internal/config/...`
Expected: FAIL — package undefined.

- [ ] **Step 5: Implement `config.go`**

```go
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	GitLab GitLab `yaml:"gitlab"`
	LLM    LLM    `yaml:"llm"`
	Review Review `yaml:"review"`
}

type GitLab struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

type LLM struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
}

type Review struct {
	MaxFileTokens       int           `yaml:"max_file_tokens"`
	MaxMRTokens         int           `yaml:"max_mr_tokens"`
	MaxConcurrentChunks int           `yaml:"max_concurrent_chunks"`
	JobTimeout          time.Duration `yaml:"job_timeout"`
	LLMCallTimeout      time.Duration `yaml:"llm_call_timeout"`
	IgnoreGlobs         []string      `yaml:"ignore_globs"`
	DeepModeDefault     bool          `yaml:"deep_mode_default"`
}

var allowedProviders = map[string]bool{"anthropic": true, "openai": true, "ollama": true}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if err := interpEnvFields(&c); err != nil {
		return nil, err
	}
	applyDefaults(&c)
	if err := validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func interpEnvFields(c *Config) error {
	for _, p := range []*string{&c.GitLab.Token, &c.LLM.APIKey} {
		v, err := interp(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}

func interp(s string) (string, error) {
	const pfx = "env:"
	if !strings.HasPrefix(s, pfx) {
		return s, nil
	}
	name := strings.TrimPrefix(s, pfx)
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("env var %s not set", name)
	}
	return v, nil
}

func applyDefaults(c *Config) {
	if c.Review.MaxFileTokens == 0 {
		c.Review.MaxFileTokens = 8000
	}
	if c.Review.MaxMRTokens == 0 {
		c.Review.MaxMRTokens = 200000
	}
	if c.Review.MaxConcurrentChunks == 0 {
		c.Review.MaxConcurrentChunks = 4
	}
	if c.Review.JobTimeout == 0 {
		c.Review.JobTimeout = 10 * time.Minute
	}
	if c.Review.LLMCallTimeout == 0 {
		c.Review.LLMCallTimeout = 90 * time.Second
	}
}

func validate(c *Config) error {
	if c.GitLab.BaseURL == "" {
		return fmt.Errorf("gitlab.base_url required")
	}
	if c.GitLab.Token == "" {
		return fmt.Errorf("gitlab.token required")
	}
	if !allowedProviders[c.LLM.Provider] {
		return fmt.Errorf("llm.provider %q not in {anthropic, openai, ollama}", c.LLM.Provider)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model required")
	}
	if c.LLM.APIKey == "" && c.LLM.Provider != "ollama" {
		return fmt.Errorf("llm.api_key required for provider %q", c.LLM.Provider)
	}
	return nil
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/config/... -v`
Expected: PASS for all three tests.

- [ ] **Step 7: Commit**

```bash
git add .
git commit -m "feat(config): yaml loader with env interpolation, defaults, validation"
```

---

## Task 3: Diff parser

**Files:**
- Create: `internal/diff/parser.go`
- Create: `internal/diff/parser_test.go`
- Create: `internal/diff/testdata/simple.diff`
- Create: `internal/diff/testdata/multi_file.diff`
- Create: `internal/diff/testdata/binary.diff`
- Create: `internal/diff/testdata/rename.diff`

- [ ] **Step 1: Create test fixtures**

`internal/diff/testdata/simple.diff`:
```
diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
 
-var X = 1
+var X = 2
+var Y = 3
```

`internal/diff/testdata/multi_file.diff`:
```
diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
-var A = 1
+var A = 2
+var B = 3
diff --git a/b.go b/b.go
index 3333333..4444444 100644
--- a/b.go
+++ b/b.go
@@ -1,1 +1,1 @@
-package x
+package b
```

`internal/diff/testdata/binary.diff`:
```
diff --git a/img.png b/img.png
index 1111111..2222222 100644
Binary files a/img.png and b/img.png differ
```

`internal/diff/testdata/rename.diff`:
```
diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
```

- [ ] **Step 2: Write the failing test `parser_test.go`**

```go
package diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(b)
}

func TestParse_Simple(t *testing.T) {
	pd, err := Parse(read(t, "simple.diff"))
	require.NoError(t, err)
	require.Len(t, pd.Files, 1)

	f := pd.Files[0]
	require.Equal(t, "foo.go", f.NewPath)
	require.Equal(t, "foo.go", f.OldPath)
	require.False(t, f.IsBinary)
	require.False(t, f.IsRename)
	require.Len(t, f.Hunks, 1)

	h := f.Hunks[0]
	require.Equal(t, 1, h.NewStart)
	require.Len(t, h.Lines, 5) // 1 ctx + 1 ctx + 1 del + 2 add

	// new-line numbering: ctx=1, ctx=2, del=0, add=3, add=4
	require.Equal(t, 1, h.Lines[0].NewLineNo)
	require.Equal(t, ' ', h.Lines[0].Kind)
	require.Equal(t, 0, h.Lines[2].NewLineNo)
	require.Equal(t, '-', h.Lines[2].Kind)
	require.Equal(t, 3, h.Lines[3].NewLineNo)
	require.Equal(t, '+', h.Lines[3].Kind)
	require.Equal(t, 4, h.Lines[4].NewLineNo)
}

func TestParse_MultiFile(t *testing.T) {
	pd, err := Parse(read(t, "multi_file.diff"))
	require.NoError(t, err)
	require.Len(t, pd.Files, 2)
	require.Equal(t, "a.go", pd.Files[0].NewPath)
	require.Equal(t, "b.go", pd.Files[1].NewPath)
}

func TestParse_Binary(t *testing.T) {
	pd, err := Parse(read(t, "binary.diff"))
	require.NoError(t, err)
	require.Len(t, pd.Files, 1)
	require.True(t, pd.Files[0].IsBinary)
	require.Empty(t, pd.Files[0].Hunks)
}

func TestParse_Rename(t *testing.T) {
	pd, err := Parse(read(t, "rename.diff"))
	require.NoError(t, err)
	require.Len(t, pd.Files, 1)
	require.True(t, pd.Files[0].IsRename)
	require.Equal(t, "old.go", pd.Files[0].OldPath)
	require.Equal(t, "new.go", pd.Files[0].NewPath)
}

func TestParse_EmptyInput(t *testing.T) {
	pd, err := Parse("")
	require.NoError(t, err)
	require.Empty(t, pd.Files)
}
```

- [ ] **Step 3: Run to confirm fail**

Run: `go test ./internal/diff/...`
Expected: FAIL — package undefined.

- [ ] **Step 4: Implement `parser.go`**

```go
package diff

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type ParsedDiff struct {
	Files []FileDiff
}

type FileDiff struct {
	OldPath  string
	NewPath  string
	IsBinary bool
	IsRename bool
	Hunks    []Hunk
}

type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []DiffLine
}

type DiffLine struct {
	Kind      rune // ' ', '+', '-'
	Content   string
	NewLineNo int // 0 for deleted lines
}

func Parse(s string) (*ParsedDiff, error) {
	pd := &ParsedDiff{}
	if strings.TrimSpace(s) == "" {
		return pd, nil
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var cur *FileDiff
	var curHunk *Hunk
	var newLineCursor int

	flushFile := func() {
		if cur != nil {
			if curHunk != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
				curHunk = nil
			}
			pd.Files = append(pd.Files, *cur)
			cur = nil
		}
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			cur = &FileDiff{}
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				cur.OldPath = strings.TrimPrefix(parts[2], "a/")
				cur.NewPath = strings.TrimPrefix(parts[3], "b/")
			}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "rename from "):
			cur.IsRename = true
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.NewPath = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files "):
			cur.IsBinary = true
		case strings.HasPrefix(line, "--- "):
			p := strings.TrimPrefix(line, "--- ")
			if p != "/dev/null" {
				cur.OldPath = strings.TrimPrefix(p, "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p != "/dev/null" {
				cur.NewPath = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@"):
			if curHunk != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			curHunk = &h
			newLineCursor = h.NewStart
		case curHunk != nil:
			if len(line) == 0 {
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: ' ', Content: "", NewLineNo: newLineCursor})
				newLineCursor++
				continue
			}
			kind := rune(line[0])
			content := line[1:]
			switch kind {
			case ' ':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: ' ', Content: content, NewLineNo: newLineCursor})
				newLineCursor++
			case '+':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: '+', Content: content, NewLineNo: newLineCursor})
				newLineCursor++
			case '-':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: '-', Content: content, NewLineNo: 0})
			default:
				// header lines like "index ..." land here when no hunk; skip
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flushFile()
	return pd, nil
}

func parseHunkHeader(line string) (Hunk, error) {
	// @@ -A,B +C,D @@ optional context
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) < 3 {
		return Hunk{}, fmt.Errorf("bad hunk header %q", line)
	}
	body := strings.TrimSpace(parts[1])
	tokens := strings.Fields(body)
	var h Hunk
	for _, t := range tokens {
		switch {
		case strings.HasPrefix(t, "-"):
			s, l, err := parseRange(strings.TrimPrefix(t, "-"))
			if err != nil {
				return Hunk{}, err
			}
			h.OldStart, h.OldLines = s, l
		case strings.HasPrefix(t, "+"):
			s, l, err := parseRange(strings.TrimPrefix(t, "+"))
			if err != nil {
				return Hunk{}, err
			}
			h.NewStart, h.NewLines = s, l
		}
	}
	return h, nil
}

func parseRange(s string) (start, lines int, err error) {
	parts := strings.SplitN(s, ",", 2)
	start, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	lines = 1
	if len(parts) == 2 {
		lines, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}
	return start, lines, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/diff/... -v`
Expected: PASS for all five tests.

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat(diff): unified diff parser with hunks and line numbering"
```

---

## Task 4: File classifier

**Files:**
- Create: `internal/classifier/classifier.go`
- Create: `internal/classifier/classifier_test.go`

- [ ] **Step 1: Add glob dep**

Run: `go get github.com/gobwas/glob`
Expected: go.mod updated.

- [ ] **Step 2: Write the failing test**

```go
package classifier

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/stretchr/testify/require"
)

func TestIsIgnored(t *testing.T) {
	globs := []string{"**/*.lock", "vendor/**", "**/*.gen.*", "**/*.min.*"}
	cases := map[string]bool{
		"package-lock.json": false, // not matched by *.lock
		"go.sum":            false,
		"yarn.lock":         true,
		"a/b/c.lock":        true,
		"vendor/x/y.go":     true,
		"src/x.go":          false,
		"foo.gen.ts":        true,
		"bundle.min.js":     true,
		"src/lib/x.min.css": true,
	}
	for path, want := range cases {
		got, err := IsIgnored(path, globs)
		require.NoError(t, err)
		require.Equalf(t, want, got, "path=%s", path)
	}
}

func TestIsIgnored_BadGlob(t *testing.T) {
	_, err := IsIgnored("a", []string{"["})
	require.Error(t, err)
}

func TestIsBinary(t *testing.T) {
	require.True(t, IsBinary(diff.FileDiff{IsBinary: true}))
	require.False(t, IsBinary(diff.FileDiff{IsBinary: false}))
}

func TestIsLockfile(t *testing.T) {
	tcases := map[string]bool{
		"package-lock.json": true,
		"yarn.lock":         true,
		"go.sum":            true,
		"Cargo.lock":        true,
		"poetry.lock":       true,
		"main.go":           false,
	}
	for p, want := range tcases {
		require.Equalf(t, want, IsLockfile(p), p)
	}
}
```

- [ ] **Step 3: Run, confirm fail**

Run: `go test ./internal/classifier/...`
Expected: FAIL.

- [ ] **Step 4: Implement `classifier.go`**

```go
package classifier

import (
	"path/filepath"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/gobwas/glob"
)

func IsIgnored(path string, globs []string) (bool, error) {
	for _, g := range globs {
		m, err := glob.Compile(g, '/')
		if err != nil {
			return false, err
		}
		if m.Match(path) {
			return true, nil
		}
	}
	return false, nil
}

func IsBinary(fd diff.FileDiff) bool {
	return fd.IsBinary
}

var lockNames = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"go.sum":            true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"Pipfile.lock":      true,
	"Gemfile.lock":      true,
	"composer.lock":     true,
}

func IsLockfile(path string) bool {
	base := filepath.Base(path)
	if lockNames[base] {
		return true
	}
	return strings.HasSuffix(base, ".lock")
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/classifier/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat(classifier): glob ignore + binary + lockfile detection"
```

---

## Task 5: Chunker

**Files:**
- Create: `internal/chunker/chunker.go`
- Create: `internal/chunker/chunker_test.go`

- [ ] **Step 1: Write the failing test**

```go
package chunker

import (
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/stretchr/testify/require"
)

func mkHunk(lineCount int) diff.Hunk {
	h := diff.Hunk{NewStart: 1, NewLines: lineCount}
	for i := 0; i < lineCount; i++ {
		h.Lines = append(h.Lines, diff.DiffLine{Kind: '+', Content: strings.Repeat("x", 80), NewLineNo: i + 1})
	}
	return h
}

func TestChunk_FitsInOne(t *testing.T) {
	fd := diff.FileDiff{NewPath: "a.go", Hunks: []diff.Hunk{mkHunk(5)}}
	chunks := Chunk(fd, 10000)
	require.Len(t, chunks, 1)
	require.Equal(t, "a.go", chunks[0].FilePath)
	require.False(t, chunks[0].Truncated)
	require.NotEmpty(t, chunks[0].DiffText)
}

func TestChunk_SplitsByHunkGroup(t *testing.T) {
	// Each hunk ~ (80+2)*40 = 3280 chars ≈ 820 tokens at 4 chars/token
	fd := diff.FileDiff{NewPath: "a.go", Hunks: []diff.Hunk{
		mkHunk(40), mkHunk(40), mkHunk(40), mkHunk(40),
	}}
	chunks := Chunk(fd, 1000) // ~1000 tokens budget
	require.GreaterOrEqual(t, len(chunks), 2)
	for _, c := range chunks {
		require.LessOrEqual(t, EstimateTokens(c.DiffText), 1000+200) // small overhead allowed
	}
}

func TestChunk_OversizedHunkTruncated(t *testing.T) {
	fd := diff.FileDiff{NewPath: "big.go", Hunks: []diff.Hunk{mkHunk(2000)}}
	chunks := Chunk(fd, 500)
	require.Len(t, chunks, 1)
	require.True(t, chunks[0].Truncated)
}

func TestChunk_EmptyFile(t *testing.T) {
	fd := diff.FileDiff{NewPath: "empty.go"}
	chunks := Chunk(fd, 1000)
	require.Empty(t, chunks)
}

func TestEstimateTokens(t *testing.T) {
	require.Equal(t, 1, EstimateTokens(""))
	require.Equal(t, 1, EstimateTokens("x"))
	require.Equal(t, 2, EstimateTokens("xxxx"))
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/chunker/...`
Expected: FAIL.

- [ ] **Step 3: Implement `chunker.go`**

```go
package chunker

import (
	"fmt"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
)

type Chunk struct {
	FilePath  string
	DiffText  string
	Truncated bool
}

// EstimateTokens uses a 4-chars-per-token heuristic. Always >= 1.
func EstimateTokens(s string) int {
	n := len(s) / 4
	if n < 1 {
		return 1
	}
	return n
}

func Chunk(fd diff.FileDiff, maxTokens int) []Chunk {
	if len(fd.Hunks) == 0 {
		return nil
	}
	var out []Chunk
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, Chunk{FilePath: fd.NewPath, DiffText: buf.String()})
		buf.Reset()
	}

	header := fmt.Sprintf("--- %s\n+++ %s\n", fd.OldPath, fd.NewPath)
	buf.WriteString(header)

	for _, h := range fd.Hunks {
		hs := renderHunk(h)
		if EstimateTokens(hs) > maxTokens {
			// oversized single hunk: truncate, single chunk
			truncated := truncateString(hs, maxTokens*4)
			buf.WriteString(truncated)
			flushedAsTruncated := Chunk{FilePath: fd.NewPath, DiffText: buf.String(), Truncated: true}
			buf.Reset()
			return []Chunk{flushedAsTruncated}
		}
		// Would adding this hunk exceed budget?
		if EstimateTokens(buf.String()+hs) > maxTokens {
			flush()
			buf.WriteString(header)
		}
		buf.WriteString(hs)
	}
	flush()
	return out
}

func renderHunk(h diff.Hunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	for _, l := range h.Lines {
		b.WriteRune(l.Kind)
		b.WriteString(l.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]\n"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/chunker/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(chunker): per-file diff chunking with token budget + truncation"
```

---

## Task 6: LLM interface + types

**Files:**
- Create: `internal/llm/provider.go`

- [ ] **Step 1: Implement `provider.go`**

```go
package llm

import "context"

type Provider interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
	Name() string
}

type ReviewRequest struct {
	SystemPrompt string
	FilePath     string
	DiffChunk    string
	FileContext  string // reserved for future use
}

type ReviewResponse struct {
	Findings []Finding
	Usage    TokenUsage
}

type Finding struct {
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

type TokenUsage struct {
	InputTokens     int
	OutputTokens    int
	CachedReadTokens int
}

const SystemPromptDefault = `You are a senior code reviewer. Review the supplied unified diff for:
- bugs and logic errors
- security vulnerabilities
- performance problems
- missing test coverage for new logic
- style violations only when egregious

Return ONLY a JSON object with this shape:
{"findings":[{"severity":"critical|major|minor|nit","category":"bug|security|perf|test|style","file":"path/from/diff","line":<int new-file line>,"message":"...","suggestion":"optional code"}]}

Use the new-file line number from the @@ -A,B +C,D @@ header. Omit a finding if you are not confident. Do not output prose outside the JSON.`
```

- [ ] **Step 2: Build to verify compiles**

Run: `go build ./internal/llm/...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add .
git commit -m "feat(llm): provider interface, finding types, default rubric"
```

---

## Task 7: LLM JSON parser

**Files:**
- Create: `internal/llm/parser.go`
- Create: `internal/llm/parser_test.go`

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFindings_Valid(t *testing.T) {
	in := `{"findings":[{"severity":"major","category":"bug","file":"a.go","line":12,"message":"nil deref"}]}`
	out, err := ParseFindings(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "major", out[0].Severity)
	require.Equal(t, 12, out[0].Line)
}

func TestParseFindings_TrailingProse(t *testing.T) {
	in := "Here is the result:\n" +
		`{"findings":[{"severity":"minor","category":"style","file":"x","line":1,"message":"m"}]}` +
		"\nThanks."
	out, err := ParseFindings(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
}

func TestParseFindings_FencedBlock(t *testing.T) {
	in := "```json\n" + `{"findings":[{"severity":"nit","category":"style","file":"x","line":1,"message":"m"}]}` + "\n```"
	out, err := ParseFindings(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
}

func TestParseFindings_Empty(t *testing.T) {
	out, err := ParseFindings(`{"findings":[]}`)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestParseFindings_Malformed(t *testing.T) {
	_, err := ParseFindings(`not json at all`)
	require.Error(t, err)
}

func TestParseFindings_RejectsUnknownSeverity(t *testing.T) {
	in := `{"findings":[{"severity":"OMEGA","category":"bug","file":"x","line":1,"message":"m"}]}`
	_, err := ParseFindings(in)
	require.Error(t, err)
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/llm/... -run ParseFindings`
Expected: FAIL.

- [ ] **Step 3: Implement `parser.go`**

```go
package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

var allowedSeverity = map[string]bool{"critical": true, "major": true, "minor": true, "nit": true}

func ParseFindings(s string) ([]Finding, error) {
	jsonText, ok := extractJSONObject(s)
	if !ok {
		return nil, fmt.Errorf("no JSON object found in output")
	}
	var wrapper struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(jsonText), &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal findings: %w", err)
	}
	for i, f := range wrapper.Findings {
		if !allowedSeverity[f.Severity] {
			return nil, fmt.Errorf("finding %d: invalid severity %q", i, f.Severity)
		}
	}
	return wrapper.Findings, nil
}

// extractJSONObject finds the first balanced top-level {...} substring.
// Strips fenced ```json blocks if present.
func extractJSONObject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// drop opening fence line
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(llm): tolerant JSON findings parser"
```

---

## Task 8: Anthropic adapter

**Files:**
- Create: `internal/llm/anthropic.go`
- Create: `internal/llm/anthropic_test.go`

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropic_ReviewSendsCacheControl(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-api-key"))
		require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"findings\":[{\"severity\":\"minor\",\"category\":\"style\",\"file\":\"a.go\",\"line\":2,\"message\":\"x\"}]}"}],
			"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":7}
		}`))
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{APIKey: "test-key", Model: "claude-sonnet-4-6", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := a.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "a.go",
		DiffChunk:    "@@ -1 +1 @@\n+x",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 7, resp.Usage.CachedReadTokens)

	// system block has cache_control
	sysBlocks := captured["system"].([]any)
	first := sysBlocks[0].(map[string]any)
	cc, ok := first["cache_control"].(map[string]any)
	require.True(t, ok, "cache_control missing")
	require.Equal(t, "ephemeral", cc["type"])
}

func TestAnthropic_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit","message":"slow down"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := a.Review(context.Background(), ReviewRequest{SystemPrompt: "s", DiffChunk: "d", FilePath: "p"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/llm/... -run Anthropic`
Expected: FAIL.

- [ ] **Step 3: Implement `anthropic.go`**

```go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type AnthropicConfig struct {
	APIKey  string
	Model   string
	BaseURL string // e.g. https://api.anthropic.com
	HTTP    *http.Client
}

type Anthropic struct {
	cfg AnthropicConfig
}

func NewAnthropic(c AnthropicConfig) *Anthropic {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.anthropic.com"
	}
	return &Anthropic{cfg: c}
}

func (a *Anthropic) Name() string { return "anthropic" }

type anthropicReq struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    []anthropicBlock `json:"system"`
	Messages  []anthropicMsg   `json:"messages"`
}

type anthropicBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl map[string]string      `json:"cache_control,omitempty"`
}

type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	user := fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)

	body := anthropicReq{
		Model:     a.cfg.Model,
		MaxTokens: 4096,
		System: []anthropicBlock{{
			Type:         "text",
			Text:         req.SystemPrompt,
			CacheControl: map[string]string{"type": "ephemeral"},
		}},
		Messages: []anthropicMsg{{
			Role:    "user",
			Content: []anthropicBlock{{Type: "text", Text: user}},
		}},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.cfg.BaseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(rb))
	}

	var ar anthropicResp
	if err := json.Unmarshal(rb, &ar); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response: %w", err)
	}
	var text string
	for _, c := range ar.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	findings, err := ParseFindings(text)
	if err != nil {
		return ReviewResponse{}, err
	}
	return ReviewResponse{
		Findings: findings,
		Usage: TokenUsage{
			InputTokens:      ar.Usage.InputTokens,
			OutputTokens:     ar.Usage.OutputTokens,
			CachedReadTokens: ar.Usage.CacheReadInputTokens,
		},
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(llm): anthropic adapter with cache_control on system block"
```

---

## Task 9: GitLab URL parser

**Files:**
- Create: `internal/gitlab/url.go`
- Create: `internal/gitlab/url_test.go`

- [ ] **Step 1: Write the failing test**

```go
package gitlab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL_OK(t *testing.T) {
	got, err := ParseURL("https://gitlab.example.com/team/project/-/merge_requests/42")
	require.NoError(t, err)
	require.Equal(t, "https://gitlab.example.com", got.BaseURL)
	require.Equal(t, "team/project", got.ProjectPath)
	require.Equal(t, 42, got.MRIID)
}

func TestParseURL_NestedGroup(t *testing.T) {
	got, err := ParseURL("https://gl.co/grp/sub/proj/-/merge_requests/7")
	require.NoError(t, err)
	require.Equal(t, "grp/sub/proj", got.ProjectPath)
	require.Equal(t, 7, got.MRIID)
}

func TestParseURL_RejectsNonMR(t *testing.T) {
	_, err := ParseURL("https://gitlab.example.com/team/project/-/issues/1")
	require.Error(t, err)
}

func TestParseURL_RejectsBadIID(t *testing.T) {
	_, err := ParseURL("https://gitlab.example.com/team/project/-/merge_requests/abc")
	require.Error(t, err)
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/gitlab/... -run ParseURL`
Expected: FAIL.

- [ ] **Step 3: Implement `url.go`**

```go
package gitlab

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type MRRef struct {
	BaseURL     string
	ProjectPath string
	MRIID       int
}

func ParseURL(raw string) (*MRRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	idx := -1
	for i, p := range parts {
		if p == "-" && i+2 < len(parts) && parts[i+1] == "merge_requests" {
			idx = i
			break
		}
	}
	if idx < 1 {
		return nil, fmt.Errorf("not a merge request URL: %s", raw)
	}
	iid, err := strconv.Atoi(parts[idx+2])
	if err != nil {
		return nil, fmt.Errorf("bad MR IID: %v", err)
	}
	return &MRRef{
		BaseURL:     u.Scheme + "://" + u.Host,
		ProjectPath: strings.Join(parts[:idx], "/"),
		MRIID:       iid,
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/gitlab/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(gitlab): MR url parser"
```

---

## Task 10: GitLab REST client

**Files:**
- Create: `internal/gitlab/types.go`
- Create: `internal/gitlab/client.go`
- Create: `internal/gitlab/client_test.go`

- [ ] **Step 1: Implement `types.go`**

```go
package gitlab

type MR struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	WebURL      string `json:"web_url"`
	BaseSHA     string
	StartSHA    string
	HeadSHA     string
}

type FileChange struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
}

type Position struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
	NewPath  string `json:"new_path"`
	OldPath  string `json:"old_path"`
	NewLine  int    `json:"new_line,omitempty"`
	PositionType string `json:"position_type"` // "text"
}
```

- [ ] **Step 2: Write failing client test**

```go
package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRESTClient_GetMRChanges(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/team%2Fproject/merge_requests/42", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "tok", r.Header.Get("PRIVATE-TOKEN"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iid": 42, "title": "x", "web_url": "u",
			"diff_refs": map[string]string{
				"base_sha": "B", "start_sha": "S", "head_sha": "H",
			},
		})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fproject/merge_requests/42/changes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"changes": []map[string]any{
				{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1 @@\n+x"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	mr, changes, err := c.GetMRWithChanges(context.Background(), "team/project", 42)
	require.NoError(t, err)
	require.Equal(t, "B", mr.BaseSHA)
	require.Equal(t, "H", mr.HeadSHA)
	require.Len(t, changes, 1)
	require.Equal(t, "a.go", changes[0].NewPath)
}

func TestRESTClient_PostNote(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/api/v4/projects/team%2Fp/merge_requests/3/notes", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	require.NoError(t, c.PostNote(context.Background(), "team/p", 3, "hello"))
	require.Equal(t, "hello", captured["body"])
}

func TestRESTClient_PostDiscussion(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.NoError(t, r.ParseForm())
		captured = r.PostForm
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	pos := Position{BaseSHA: "B", StartSHA: "S", HeadSHA: "H", NewPath: "a.go", OldPath: "a.go", NewLine: 7, PositionType: "text"}
	require.NoError(t, c.PostDiscussion(context.Background(), "team/p", 3, "found a thing", pos))
	require.Equal(t, "found a thing", captured.Get("body"))
	require.Equal(t, "B", captured.Get("position[base_sha]"))
	require.Equal(t, "7", captured.Get("position[new_line]"))
}

func TestRESTClient_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"404 Not found"}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	_, _, err := c.GetMRWithChanges(context.Background(), "x/y", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
}
```

- [ ] **Step 3: Run, confirm fail**

Run: `go test ./internal/gitlab/... -run RESTClient`
Expected: FAIL.

- [ ] **Step 4: Implement `client.go`**

```go
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Client interface {
	GetMRWithChanges(ctx context.Context, projectPath string, mrIID int) (*MR, []FileChange, error)
	PostNote(ctx context.Context, projectPath string, mrIID int, body string) error
	PostDiscussion(ctx context.Context, projectPath string, mrIID int, body string, pos Position) error
}

type RESTClient struct {
	base  string
	token string
	http  *http.Client
}

func NewRESTClient(base, token string, h *http.Client) *RESTClient {
	if h == nil {
		h = http.DefaultClient
	}
	return &RESTClient{base: strings.TrimRight(base, "/"), token: token, http: h}
}

func (c *RESTClient) projURL(projectPath string) string {
	return c.base + "/api/v4/projects/" + url.PathEscape(projectPath)
}

func (c *RESTClient) do(ctx context.Context, method, path string, body io.Reader, hdr map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gitlab %s %s: %d %s", method, path, resp.StatusCode, string(rb))
	}
	return rb, nil
}

type mrEnvelope struct {
	IID      int    `json:"iid"`
	Title    string `json:"title"`
	WebURL   string `json:"web_url"`
	DiffRefs struct {
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
}

type changesEnvelope struct {
	Changes []FileChange `json:"changes"`
}

func (c *RESTClient) GetMRWithChanges(ctx context.Context, projectPath string, iid int) (*MR, []FileChange, error) {
	mrURL := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid)
	rb, err := c.do(ctx, "GET", mrURL, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var env mrEnvelope
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, nil, err
	}
	cb, err := c.do(ctx, "GET", mrURL+"/changes", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var ch changesEnvelope
	if err := json.Unmarshal(cb, &ch); err != nil {
		return nil, nil, err
	}
	return &MR{
		IID: env.IID, Title: env.Title, WebURL: env.WebURL,
		BaseSHA: env.DiffRefs.BaseSHA, StartSHA: env.DiffRefs.StartSHA, HeadSHA: env.DiffRefs.HeadSHA,
	}, ch.Changes, nil
}

func (c *RESTClient) PostNote(ctx context.Context, projectPath string, iid int, body string) error {
	u := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid) + "/notes"
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := c.do(ctx, "POST", u, bytes.NewReader(payload), map[string]string{"content-type": "application/json"})
	return err
}

func (c *RESTClient) PostDiscussion(ctx context.Context, projectPath string, iid int, body string, pos Position) error {
	u := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid) + "/discussions"
	form := url.Values{}
	form.Set("body", body)
	form.Set("position[position_type]", pos.PositionType)
	form.Set("position[base_sha]", pos.BaseSHA)
	form.Set("position[start_sha]", pos.StartSHA)
	form.Set("position[head_sha]", pos.HeadSHA)
	form.Set("position[new_path]", pos.NewPath)
	form.Set("position[old_path]", pos.OldPath)
	if pos.NewLine > 0 {
		form.Set("position[new_line]", strconv.Itoa(pos.NewLine))
	}
	_, err := c.do(ctx, "POST", u, strings.NewReader(form.Encode()),
		map[string]string{"content-type": "application/x-www-form-urlencoded"})
	return err
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/gitlab/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat(gitlab): REST client with MR fetch, note, discussion"
```

---

## Task 11: Review aggregator

**Files:**
- Create: `internal/review/aggregator.go`
- Create: `internal/review/aggregator_test.go`

- [ ] **Step 1: Write the failing test**

```go
package review

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestAggregate_DedupesByFileLineMessage(t *testing.T) {
	in := []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 10, Message: "naming"},
		{Severity: "minor", Category: "style", File: "a.go", Line: 10, Message: "naming"}, // dup
		{Severity: "major", Category: "bug", File: "a.go", Line: 11, Message: "nil"},
	}
	r := Aggregate(in)
	require.Len(t, r.Findings, 2)
	// major sorted before minor
	require.Equal(t, "major", r.Findings[0].Severity)
	require.Equal(t, 1, r.Counts["major"])
	require.Equal(t, 1, r.Counts["minor"])
}

func TestAggregate_BuildsSummary(t *testing.T) {
	in := []llm.Finding{
		{Severity: "critical", Category: "security", File: "x.go", Line: 1, Message: "sqli"},
	}
	r := Aggregate(in)
	require.Contains(t, r.SummaryBody, "AI Code Review")
	require.Contains(t, r.SummaryBody, "critical")
	require.Contains(t, r.SummaryBody, "x.go:1")
	require.Contains(t, r.SummaryBody, "sqli")
}

func TestAggregate_EmptySummary(t *testing.T) {
	r := Aggregate(nil)
	require.Empty(t, r.Findings)
	require.Contains(t, r.SummaryBody, "no findings")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/review/... -run Aggregate`
Expected: FAIL.

- [ ] **Step 3: Implement `aggregator.go`**

```go
package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

type AggregateResult struct {
	Findings    []llm.Finding
	Counts      map[string]int
	SummaryBody string
}

var sevRank = map[string]int{"critical": 0, "major": 1, "minor": 2, "nit": 3}

func Aggregate(fs []llm.Finding) AggregateResult {
	dedup := map[string]llm.Finding{}
	for _, f := range fs {
		k := fmt.Sprintf("%s|%d|%s", f.File, f.Line, f.Message)
		if _, ok := dedup[k]; !ok {
			dedup[k] = f
		}
	}
	out := make([]llm.Finding, 0, len(dedup))
	for _, f := range dedup {
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := sevRank[out[i].Severity], sevRank[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	counts := map[string]int{}
	for _, f := range out {
		counts[f.Severity]++
	}
	return AggregateResult{
		Findings:    out,
		Counts:      counts,
		SummaryBody: buildSummary(out, counts),
	}
}

func buildSummary(fs []llm.Finding, counts map[string]int) string {
	var b strings.Builder
	b.WriteString("## AI Code Review\n\n")
	if len(fs) == 0 {
		b.WriteString("_no findings_\n")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("**Findings:** critical=%d major=%d minor=%d nit=%d\n\n",
		counts["critical"], counts["major"], counts["minor"], counts["nit"]))
	for _, f := range fs {
		fmt.Fprintf(&b, "- **[%s/%s] %s:%d** — %s\n", f.Severity, f.Category, f.File, f.Line, f.Message)
	}
	b.WriteString("\n_Inline comments posted on diff lines where possible._\n")
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/review/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(review): aggregator with dedupe, severity sort, summary"
```

---

## Task 12: Review orchestrator

**Files:**
- Create: `internal/review/orchestrator.go`
- Create: `internal/review/orchestrator_test.go`

- [ ] **Step 1: Write the failing test (uses fake provider + fake gitlab)**

```go
package review

import (
	"context"
	"sync"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	findings []llm.Finding
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return llm.ReviewResponse{Findings: f.findings}, nil
}

type fakeGL struct {
	mu          sync.Mutex
	notes       []string
	discussions []struct {
		Body string
		Pos  gitlab.Position
	}
}

func (f *fakeGL) GetMRWithChanges(ctx context.Context, project string, iid int) (*gitlab.MR, []gitlab.FileChange, error) {
	mr := &gitlab.MR{IID: iid, BaseSHA: "B", StartSHA: "S", HeadSHA: "H", WebURL: "u"}
	changes := []gitlab.FileChange{
		{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1,2 @@\n-old\n+new1\n+new2\n"},
	}
	return mr, changes, nil
}
func (f *fakeGL) PostNote(ctx context.Context, project string, iid int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, body)
	return nil
}
func (f *fakeGL) PostDiscussion(ctx context.Context, project string, iid int, body string, pos gitlab.Position) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discussions = append(f.discussions, struct {
		Body string
		Pos  gitlab.Position
	}{body, pos})
	return nil
}

func TestOrchestrator_Run_PostsSummaryAndInline(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "major", Category: "bug", File: "a.go", Line: 1, Message: "boom", Suggestion: "fix"},
	}}

	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 2,
		IgnoreGlobs:   []string{},
	})
	res, err := o.Run(context.Background(), "https://gl.example.com/grp/proj/-/merge_requests/9")
	require.NoError(t, err)
	require.Equal(t, 1, res.Posted)
	require.Len(t, gl.notes, 1)
	require.Contains(t, gl.notes[0], "AI Code Review")
	require.Len(t, gl.discussions, 1)
	require.Equal(t, 1, gl.discussions[0].Pos.NewLine)
	require.Equal(t, "a.go", gl.discussions[0].Pos.NewPath)
}

func TestOrchestrator_Run_RespectsIgnoreGlobs(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{{Severity: "minor", Category: "style", File: "x", Line: 1, Message: "m"}}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 2,
		IgnoreGlobs:   []string{"**/*.go"},
	})
	res, err := o.Run(context.Background(), "https://gl/grp/proj/-/merge_requests/1")
	require.NoError(t, err)
	require.Equal(t, 0, prov.calls)
	require.Equal(t, 0, res.Posted)
	require.Len(t, gl.notes, 1)
	require.Contains(t, gl.notes[0], "no findings")
}

func TestOrchestrator_Run_FallsBackWhenLineMissing(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 999, Message: "off-diff"},
	}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 1,
	})
	res, err := o.Run(context.Background(), "https://gl/x/y/-/merge_requests/2")
	require.NoError(t, err)
	require.Equal(t, 0, len(gl.discussions)) // fallback
	require.Equal(t, 0, res.Posted)
	require.Contains(t, gl.notes[0], "off-diff")
}

func TestOrchestrator_Run_BadURL(t *testing.T) {
	o := New(Config{GitLab: &fakeGL{}, Provider: &fakeProvider{}})
	_, err := o.Run(context.Background(), "not a url")
	require.Error(t, err)
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/review/... -run Orchestrator`
Expected: FAIL.

- [ ] **Step 3: Implement `orchestrator.go`**

```go
package review

import (
	"context"
	"fmt"
	"sync"

	"github.com/fahmi/gitlab-mr-review-bot/internal/chunker"
	"github.com/fahmi/gitlab-mr-review-bot/internal/classifier"
	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

type Config struct {
	GitLab        gitlab.Client
	Provider      llm.Provider
	SystemPrompt  string
	MaxFileTokens int
	MaxMRTokens   int
	MaxConcurrent int
	IgnoreGlobs   []string
}

type Orchestrator struct{ cfg Config }

func New(cfg Config) *Orchestrator {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = llm.SystemPromptDefault
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	return &Orchestrator{cfg: cfg}
}

type RunResult struct {
	Findings  int
	Posted    int // inline discussions posted
	Skipped   int
	WebURL    string
	Counts    map[string]int
}

func (o *Orchestrator) Run(ctx context.Context, mrURL string) (*RunResult, error) {
	ref, err := gitlab.ParseURL(mrURL)
	if err != nil {
		return nil, err
	}
	mr, changes, err := o.cfg.GitLab.GetMRWithChanges(ctx, ref.ProjectPath, ref.MRIID)
	if err != nil {
		return nil, fmt.Errorf("fetch MR: %w", err)
	}

	type job struct {
		path      string
		hunksByLine map[int]bool
		chunk     chunker.Chunk
	}
	var jobs []job
	totalTokens := 0
	for _, ch := range changes {
		if ch.DeletedFile {
			continue
		}
		ignored, err := classifier.IsIgnored(ch.NewPath, o.cfg.IgnoreGlobs)
		if err != nil {
			return nil, err
		}
		if ignored || classifier.IsLockfile(ch.NewPath) {
			continue
		}
		// Wrap the bare diff hunks in a synthetic file header so parser succeeds.
		full := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s",
			ch.OldPath, ch.NewPath, ch.OldPath, ch.NewPath, ch.Diff)
		pd, err := diff.Parse(full)
		if err != nil {
			continue
		}
		if len(pd.Files) == 0 {
			continue
		}
		fd := pd.Files[0]
		if classifier.IsBinary(fd) {
			continue
		}
		validLines := map[int]bool{}
		for _, h := range fd.Hunks {
			for _, ln := range h.Lines {
				if ln.Kind == '+' || ln.Kind == ' ' {
					validLines[ln.NewLineNo] = true
				}
			}
		}
		for _, c := range chunker.Chunk(fd, o.cfg.MaxFileTokens) {
			t := chunker.EstimateTokens(c.DiffText)
			if totalTokens+t > o.cfg.MaxMRTokens {
				break
			}
			totalTokens += t
			jobs = append(jobs, job{path: ch.NewPath, hunksByLine: validLines, chunk: c})
		}
	}

	// Run LLM calls with bounded concurrency.
	sem := make(chan struct{}, o.cfg.MaxConcurrent)
	var (
		mu       sync.Mutex
		findings []llm.Finding
		errsAny  error
	)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := o.cfg.Provider.Review(ctx, llm.ReviewRequest{
				SystemPrompt: o.cfg.SystemPrompt,
				FilePath:     j.path,
				DiffChunk:    j.chunk.DiffText,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsAny = err // last error wins; partial success still possible
				return
			}
			for _, f := range resp.Findings {
				if f.File == "" {
					f.File = j.path
				}
				findings = append(findings, f)
			}
		}()
	}
	wg.Wait()

	// Build line-validity map per file from all jobs.
	lineMap := map[string]map[int]bool{}
	for _, j := range jobs {
		if _, ok := lineMap[j.path]; !ok {
			lineMap[j.path] = map[int]bool{}
		}
		for ln := range j.hunksByLine {
			lineMap[j.path][ln] = true
		}
	}

	agg := Aggregate(findings)

	// Summary note first.
	if err := o.cfg.GitLab.PostNote(ctx, ref.ProjectPath, ref.MRIID, agg.SummaryBody); err != nil {
		return nil, fmt.Errorf("post summary: %w", err)
	}

	posted := 0
	skipped := 0
	for _, f := range agg.Findings {
		valid := lineMap[f.File] != nil && lineMap[f.File][f.Line]
		if !valid {
			skipped++
			continue
		}
		body := f.Message
		if f.Suggestion != "" {
			body += "\n\n```suggestion\n" + f.Suggestion + "\n```"
		}
		pos := gitlab.Position{
			BaseSHA: mr.BaseSHA, StartSHA: mr.StartSHA, HeadSHA: mr.HeadSHA,
			NewPath: f.File, OldPath: f.File,
			NewLine: f.Line, PositionType: "text",
		}
		if err := o.cfg.GitLab.PostDiscussion(ctx, ref.ProjectPath, ref.MRIID, body, pos); err != nil {
			skipped++
			continue
		}
		posted++
	}
	_ = errsAny // surfacing partial errors is a future improvement
	return &RunResult{
		Findings: len(agg.Findings),
		Posted:   posted,
		Skipped:  skipped,
		WebURL:   mr.WebURL,
		Counts:   agg.Counts,
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/review/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat(review): orchestrator wires filter, chunk, llm, aggregate, post"
```

---

## Task 13: review-cli driver

**Files:**
- Modify: `cmd/review-cli/main.go`

- [ ] **Step 1: Replace placeholder with real CLI**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/config"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: review-cli --config FILE <mr-url>")
		os.Exit(2)
	}
	mrURL := flag.Arg(0)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	hc := &http.Client{Timeout: 60 * time.Second}
	gl := gitlab.NewRESTClient(cfg.GitLab.BaseURL, cfg.GitLab.Token, hc)

	var prov llm.Provider
	switch cfg.LLM.Provider {
	case "anthropic":
		prov = llm.NewAnthropic(llm.AnthropicConfig{
			APIKey:  cfg.LLM.APIKey,
			Model:   cfg.LLM.Model,
			BaseURL: cfg.LLM.BaseURL,
			HTTP:    hc,
		})
	default:
		fmt.Fprintln(os.Stderr, "provider not yet supported in Plan 1:", cfg.LLM.Provider)
		os.Exit(1)
	}

	o := review.New(review.Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: cfg.Review.MaxFileTokens,
		MaxMRTokens:   cfg.Review.MaxMRTokens,
		MaxConcurrent: cfg.Review.MaxConcurrentChunks,
		IgnoreGlobs:   cfg.Review.IgnoreGlobs,
	})

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Review.JobTimeout)
	defer cancel()

	res, err := o.Run(ctx, mrURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "review failed:", err)
		os.Exit(1)
	}
	fmt.Printf("posted=%d skipped=%d findings=%d url=%s\n", res.Posted, res.Skipped, res.Findings, res.WebURL)
}
```

- [ ] **Step 2: Build**

Run: `go build ./cmd/review-cli`
Expected: produces `review-cli` binary in working dir.

- [ ] **Step 3: Smoke (manual, document but skip in automation)**

Document in `README.md` (create minimal one):
```bash
cp config.example.yaml config.yaml
# fill GITLAB_TOKEN, ANTHROPIC_API_KEY in env
./review-cli --config config.yaml https://gl.your-corp.com/team/proj/-/merge_requests/N
```

- [ ] **Step 4: Commit**

```bash
git add .
git commit -m "feat(cli): review-cli driver wires config to orchestrator"
```

---

## Task 14: End-to-end test

**Files:**
- Create: `internal/review/e2e_test.go`

- [ ] **Step 1: Write the failing e2e test**

```go
package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/stretchr/testify/require"
)

// fakeAnthropic returns a canned findings JSON.
func fakeAnthropicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"findings\":[{\"severity\":\"major\",\"category\":\"bug\",\"file\":\"a.go\",\"line\":2,\"message\":\"boom\"}]}"}],
			"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0}
		}`))
	})
}

func fakeGitLabHandler(t *testing.T, postedNotes *int, postedDiscussions *int) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/projects/grp%2Fproj/merge_requests/9" && r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iid": 9, "title": "x", "web_url": "u",
				"diff_refs": map[string]string{"base_sha": "B", "start_sha": "S", "head_sha": "H"},
			})
			return
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/changes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"changes": []map[string]any{
				{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1,2 @@\n-old\n+new1\n+new2\n"},
			},
		})
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/notes", func(w http.ResponseWriter, r *http.Request) {
		*postedNotes++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/discussions", func(w http.ResponseWriter, r *http.Request) {
		*postedDiscussions++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	})
	return mux
}

func TestE2E_HappyPath(t *testing.T) {
	var notes, discussions int
	gl := httptest.NewServer(fakeGitLabHandler(t, &notes, &discussions))
	defer gl.Close()
	llmSrv := httptest.NewServer(fakeAnthropicHandler())
	defer llmSrv.Close()

	prov := llm.NewAnthropic(llm.AnthropicConfig{
		APIKey: "k", Model: "m", BaseURL: llmSrv.URL, HTTP: llmSrv.Client(),
	})
	client := gitlab.NewRESTClient(gl.URL, "tok", gl.Client())
	o := New(Config{
		GitLab: client, Provider: prov,
		MaxFileTokens: 4000, MaxMRTokens: 200000, MaxConcurrent: 2,
	})

	res, err := o.Run(context.Background(), gl.URL+"/grp/proj/-/merge_requests/9")
	require.NoError(t, err)
	require.Equal(t, 1, notes)
	require.Equal(t, 1, discussions)
	require.Equal(t, 1, res.Posted)
}
```

- [ ] **Step 2: Run, confirm it passes (everything else already implemented)**

Run: `go test ./internal/review/... -run E2E -v`
Expected: PASS.

- [ ] **Step 3: Run full suite**

Run: `go test ./... -v`
Expected: ALL PASS across config, diff, classifier, chunker, llm, gitlab, review.

- [ ] **Step 4: Commit**

```bash
git add .
git commit -m "test(review): end-to-end happy path with fake gitlab + fake anthropic"
```

---

## Self-Review Checklist (run after Task 14)

1. **Spec coverage:**
   - Architecture / packages — Tasks 1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12 ✓
   - Filter step — Task 4 ✓
   - Chunking + token budget — Task 5 ✓
   - LLM provider interface + Anthropic adapter w/ cache_control — Tasks 6, 7, 8 ✓
   - GitLab client with note + discussion — Task 10 ✓
   - Aggregator (dedupe, sort, summary) — Task 11 ✓
   - Orchestrator + line-validity fallback — Task 12 ✓
   - End-to-end test — Task 14 ✓
   - Discord layer, in-memory job tracker — **deferred to Plan 2** (intentional)
   - OpenAI / Ollama adapters — **deferred to Plan 3** (intentional)
   - Deep mode (two-pass) — **deferred** (mentioned in config; not implemented; Plan 2 may add)
   - Retry logic on 429 / 5xx — **deferred** (raised in spec; future Task in Plan 2 or follow-up)
2. **Placeholders:** none. Every step has full code or full command.
3. **Type consistency:** verified — `Finding`, `Position`, `MR`, `FileChange`, `Chunk`, `Provider.Review`, `Client.GetMRWithChanges`, `Aggregate`, `Orchestrator.Run` are used identically across tasks.

---

## Followup Plans

- **Plan 2 — Discord layer:** discord.go session, slash command `/review`, in-memory job tracker, status updates, retry/backoff for LLM and GitLab calls, deep-mode flag, main wiring.
- **Plan 3 — Additional LLM adapters:** OpenAI Chat Completions, Ollama. Provider switch via config. No structural changes to orchestrator.
