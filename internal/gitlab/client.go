package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrFileNotFound = errors.New("gitlab: file not found")

type Client interface {
	GetMRWithChanges(ctx context.Context, projectPath string, mrIID int) (*MR, []FileChange, error)
	PostNote(ctx context.Context, projectPath string, mrIID int, body string) error
	PostDiscussion(ctx context.Context, projectPath string, mrIID int, body string, pos Position) error
	GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error)
}

type RESTClient struct {
	base  string
	token string
	http  *http.Client
}

func NewRESTClient(base, token string, h *http.Client) *RESTClient {
	if h == nil {
		h = http.DefaultClient
	}
	return &RESTClient{base: strings.TrimRight(base, "/"), token: token, http: h}
}

func (c *RESTClient) projURL(projectPath string) string {
	return c.base + "/api/v4/projects/" + url.PathEscape(projectPath)
}

func (c *RESTClient) do(ctx context.Context, method, path string, body io.Reader, hdr map[string]string) ([]byte, error) {
	rb, _, err := c.doWithHeader(ctx, method, path, body, hdr)
	return rb, err
}

func (c *RESTClient) doWithHeader(ctx context.Context, method, path string, body io.Reader, hdr map[string]string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.Header, fmt.Errorf("gitlab %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, resp.Header, fmt.Errorf("gitlab %s %s: %d %s", method, path, resp.StatusCode, string(rb))
	}
	return rb, resp.Header, nil
}

type mrEnvelope struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	WebURL       string `json:"web_url"`
	TargetBranch string `json:"target_branch"`
	DiffRefs     struct {
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
}

type changesEnvelope struct {
	Changes []FileChange `json:"changes"`
}

// diffsPerPage caps each /diffs page request. GitLab allows up to 500.
const diffsPerPage = 100

// diffsMaxPages bounds total pages to keep huge MRs from monopolizing memory
// and request budget. 50 pages * 100 per page = 5000 files, far past anything
// reasonable for a code review.
const diffsMaxPages = 50

func (c *RESTClient) GetMRWithChanges(ctx context.Context, projectPath string, iid int) (*MR, []FileChange, error) {
	mrURL := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid)
	rb, err := c.do(ctx, "GET", mrURL, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var env mrEnvelope
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, nil, err
	}

	changes, err := c.fetchAllDiffs(ctx, mrURL)
	if err != nil {
		return nil, nil, err
	}
	return &MR{
		IID: env.IID, Title: env.Title, WebURL: env.WebURL,
		BaseSHA: env.DiffRefs.BaseSHA, StartSHA: env.DiffRefs.StartSHA, HeadSHA: env.DiffRefs.HeadSHA,
		TargetBranch: env.TargetBranch,
	}, changes, nil
}

// fetchAllDiffs walks the paginated /diffs endpoint. Falls back to /changes for
// older GitLab instances (pre-15.7) where /diffs returns 404.
func (c *RESTClient) fetchAllDiffs(ctx context.Context, mrURL string) ([]FileChange, error) {
	var all []FileChange
	for page := 1; page <= diffsMaxPages; page++ {
		u := fmt.Sprintf("%s/diffs?per_page=%d&page=%d", mrURL, diffsPerPage, page)
		rb, hdr, err := c.doWithHeader(ctx, "GET", u, nil, nil)
		if err != nil {
			if page == 1 && isNotFound(err) {
				return c.fetchChangesFallback(ctx, mrURL)
			}
			return nil, err
		}
		var batch []FileChange
		if err := json.Unmarshal(rb, &batch); err != nil {
			return nil, fmt.Errorf("decode diffs page %d: %w", page, err)
		}
		all = append(all, batch...)
		if next := hdr.Get("X-Next-Page"); next == "" || len(batch) == 0 {
			break
		}
	}
	return all, nil
}

func (c *RESTClient) fetchChangesFallback(ctx context.Context, mrURL string) ([]FileChange, error) {
	cb, err := c.do(ctx, "GET", mrURL+"/changes", nil, nil)
	if err != nil {
		return nil, err
	}
	var ch changesEnvelope
	if err := json.Unmarshal(cb, &ch); err != nil {
		return nil, err
	}
	return ch.Changes, nil
}

func (c *RESTClient) GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error) {
	u := c.projURL(projectPath) + "/repository/files/" + url.PathEscape(filePath) + "/raw?ref=" + url.QueryEscape(ref)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return "", ErrFileNotFound
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gitlab GET %s: %d %s", u, resp.StatusCode, string(rb))
	}
	return string(rb), nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), ": 404 ")
}

func (c *RESTClient) PostNote(ctx context.Context, projectPath string, iid int, body string) error {
	u := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid) + "/notes"
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := c.do(ctx, "POST", u, bytes.NewReader(payload), map[string]string{"content-type": "application/json"})
	return err
}

func (c *RESTClient) PostDiscussion(ctx context.Context, projectPath string, iid int, body string, pos Position) error {
	u := c.projURL(projectPath) + "/merge_requests/" + strconv.Itoa(iid) + "/discussions"
	form := url.Values{}
	form.Set("body", body)
	form.Set("position[position_type]", pos.PositionType)
	form.Set("position[base_sha]", pos.BaseSHA)
	form.Set("position[start_sha]", pos.StartSHA)
	form.Set("position[head_sha]", pos.HeadSHA)
	form.Set("position[new_path]", pos.NewPath)
	form.Set("position[old_path]", pos.OldPath)
	if pos.NewLine > 0 {
		form.Set("position[new_line]", strconv.Itoa(pos.NewLine))
	}
	_, err := c.do(ctx, "POST", u, strings.NewReader(form.Encode()),
		map[string]string{"content-type": "application/x-www-form-urlencoded"})
	return err
}
