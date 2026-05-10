package discord

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/stretchr/testify/require"
)

func okHosts() map[string]bool {
	return map[string]bool{"gl.example.com": true, "gl": true}
}

func TestValidateRequest_OK(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: nil, // empty allowlist => everyone
		AllowedRoleIDs: nil,
		AllowedHosts:   okHosts(),
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
	v := Validator{Tracker: tr, AllowedHosts: okHosts()}
	_, err := v.Validate(Request{UserID: "u1", MRURL: "not a url"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBadURL)
}

func TestValidateRequest_NotAllowedUser(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: map[string]bool{"alice": true},
		AllowedHosts:   okHosts(),
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
		AllowedHosts:   okHosts(),
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
	v := Validator{Tracker: tr, AllowedHosts: okHosts()}
	_, err := v.Validate(Request{
		UserID: "u",
		MRURL:  "https://gl/x/y/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrDuplicate)
}

func TestValidateRequest_RejectsForeignHost(t *testing.T) {
	tr := jobs.New()
	v := Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}}
	_, err := v.Validate(Request{
		UserID: "u1",
		MRURL:  "https://attacker.com/team/proj/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrBadHost)
}

func TestValidateRequest_FailsClosedWithoutAllowlist(t *testing.T) {
	tr := jobs.New()
	v := Validator{Tracker: tr} // empty AllowedHosts => reject all
	_, err := v.Validate(Request{
		UserID: "u1",
		MRURL:  "https://gl.example.com/team/proj/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrBadHost)
}

func TestHostFromBaseURL(t *testing.T) {
	h, err := HostFromBaseURL("https://gl.Example.COM/api")
	require.NoError(t, err)
	require.Equal(t, "gl.example.com", h)

	_, err = HostFromBaseURL("not-a-url")
	require.Error(t, err)
}
