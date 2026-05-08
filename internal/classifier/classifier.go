package classifier

import (
	"path/filepath"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/gobwas/glob"
)

func IsIgnored(path string, globs []string) (bool, error) {
	for _, g := range globs {
		matchers, err := compilePatterns(g)
		if err != nil {
			return false, err
		}
		for _, m := range matchers {
			if m.Match(path) {
				return true, nil
			}
		}
	}
	return false, nil
}

// compilePatterns returns 1 or 2 matchers. For globs starting with "**/",
// also include the suffix-only variant so root-level files match.
func compilePatterns(g string) ([]glob.Glob, error) {
	primary, err := glob.Compile(g, '/')
	if err != nil {
		return nil, err
	}
	out := []glob.Glob{primary}
	if strings.HasPrefix(g, "**/") {
		alt, err := glob.Compile(strings.TrimPrefix(g, "**/"), '/')
		if err != nil {
			return nil, err
		}
		out = append(out, alt)
	}
	return out, nil
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
