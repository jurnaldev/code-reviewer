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
