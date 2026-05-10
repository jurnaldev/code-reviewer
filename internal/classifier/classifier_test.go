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
