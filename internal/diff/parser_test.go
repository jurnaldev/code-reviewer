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
