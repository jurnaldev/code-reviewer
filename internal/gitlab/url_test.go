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
