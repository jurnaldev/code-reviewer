package discord

import (
	"errors"
	"fmt"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
)

var (
	ErrBadURL     = errors.New("invalid MR URL")
	ErrNotAllowed = errors.New("user not allowed")
	ErrDuplicate  = errors.New("review already in progress for this MR")
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
}

func (v Validator) Validate(r Request) (ValidatedRequest, error) {
	if !v.allowed(r.UserID, r.RoleIDs) {
		return ValidatedRequest{}, ErrNotAllowed
	}
	ref, err := gitlab.ParseURL(r.MRURL)
	if err != nil {
		return ValidatedRequest{}, fmt.Errorf("%w: %v", ErrBadURL, err)
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
