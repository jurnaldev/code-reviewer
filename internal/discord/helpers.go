package discord

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
)

var (
	ErrBadURL     = errors.New("invalid MR URL")
	ErrNotAllowed = errors.New("user not allowed")
	ErrDuplicate  = errors.New("review already in progress for this MR")
	ErrBadHost    = errors.New("MR URL host not in allowlist")
)

type Request struct {
	UserID  string
	RoleIDs []string
	MRURL   string
}

type ValidatedRequest struct {
	ProjectPath string
	MRIID       int
	BaseURL     string
}

type Validator struct {
	Tracker        *jobs.Tracker
	AllowedUserIDs map[string]bool // empty => allow everyone
	AllowedRoleIDs map[string]bool // empty => no role restriction
	AllowedHosts   map[string]bool // empty => reject all (must be set explicitly)
}

func (v Validator) Validate(r Request) (ValidatedRequest, error) {
	if !v.allowed(r.UserID, r.RoleIDs) {
		return ValidatedRequest{}, ErrNotAllowed
	}
	ref, err := gitlab.ParseURL(r.MRURL)
	if err != nil {
		return ValidatedRequest{}, fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	if !v.hostAllowed(ref.BaseURL) {
		return ValidatedRequest{}, ErrBadHost
	}
	if _, ok := v.Tracker.FindActiveByMR(r.MRURL); ok {
		return ValidatedRequest{}, ErrDuplicate
	}
	return ValidatedRequest{
		ProjectPath: ref.ProjectPath,
		MRIID:       ref.MRIID,
		BaseURL:     ref.BaseURL,
	}, nil
}

// hostAllowed returns true when the parsed BaseURL's host matches an entry in
// AllowedHosts. Empty AllowedHosts rejects everything to fail closed — the bot
// holds a PRIVATE-TOKEN that must not leak to attacker-controlled hosts.
func (v Validator) hostAllowed(baseURL string) bool {
	if len(v.AllowedHosts) == 0 {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	return v.AllowedHosts[strings.ToLower(u.Host)]
}

// HostFromBaseURL extracts the lowercase host from a configured GitLab base URL
// for inclusion in Validator.AllowedHosts.
func HostFromBaseURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("base url %q has no host", base)
	}
	return strings.ToLower(u.Host), nil
}

func (v Validator) allowed(userID string, roleIDs []string) bool {
	hasUserList := len(v.AllowedUserIDs) > 0
	hasRoleList := len(v.AllowedRoleIDs) > 0
	if !hasUserList && !hasRoleList {
		return true
	}
	if hasUserList && v.AllowedUserIDs[userID] {
		return true
	}
	if hasRoleList {
		for _, rid := range roleIDs {
			if v.AllowedRoleIDs[rid] {
				return true
			}
		}
	}
	return false
}
