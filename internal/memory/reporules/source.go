package reporules

import (
	"context"
	"errors"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

const maxFileBytes = 4096

// FileGetter is the minimal slice of gitlab.Client used here.
type FileGetter interface {
	GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error)
}

type Source struct {
	gl   FileGetter
	path string
}

func New(gl FileGetter, path string) *Source {
	return &Source{gl: gl, path: path}
}

func (s *Source) Recall(ctx context.Context, mr memory.MRRef) ([]memory.Memory, error) {
	ref := mr.TargetRef
	if ref == "" {
		ref = "HEAD"
	}
	body, err := s.gl.GetFileRaw(ctx, mr.Project, s.path, ref)
	if errors.Is(err, gitlab.ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) > maxFileBytes {
		body = body[:maxFileBytes] + " _(truncated)_"
	}
	return []memory.Memory{{
		Kind:    memory.KindRule,
		Content: body,
		Project: mr.Project,
	}}, nil
}
