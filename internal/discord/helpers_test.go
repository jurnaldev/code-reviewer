package discord

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/stretchr/testify/require"
)

func TestValidateRequest_OK(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: nil, // empty allowlist => everyone
		AllowedRoleIDs: nil,
	}
	res, err := v.Validate(Request{
		UserID:  "u1",
		RoleIDs: []string{"r1"},
		MRURL:   "https://gl.example.com/team/proj/-/merge_requests/3",
	})
	require.NoError(t, err)
	require.Equal(t, "team/proj", res.ProjectPath)
	require.Equal(t, 3, res.MRIID)
}

func TestValidateRequest_BadURL(t *testing.T) {
	tr := jobs.New()
	v := Validator{Tracker: tr}
	_, err := v.Validate(Request{UserID: "u1", MRURL: "not a url"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBadURL)
}

func TestValidateRequest_NotAllowedUser(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: map[string]bool{"alice": true},
	}
	_, err := v.Validate(Request{
		UserID: "bob",
		MRURL:  "https://gl/x/y/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrNotAllowed)
}

func TestValidateRequest_AllowedByRole(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedRoleIDs: map[string]bool{"reviewers": true},
	}
	_, err := v.Validate(Request{
		UserID:  "bob",
		RoleIDs: []string{"reviewers"},
		MRURL:   "https://gl/x/y/-/merge_requests/1",
	})
	require.NoError(t, err)
}

func TestValidateRequest_Duplicate(t *testing.T) {
	tr := jobs.New()
	tr.Create("u", "https://gl/x/y/-/merge_requests/1")
	v := Validator{Tracker: tr}
	_, err := v.Validate(Request{
		UserID: "u",
		MRURL:  "https://gl/x/y/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrDuplicate)
}
