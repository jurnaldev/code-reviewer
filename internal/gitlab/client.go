package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Client interface {
	GetMRWithChanges(ctx context.Context, projectPath string, mrIID int) (*MR, []FileChange, error)
	PostNote(ctx context.Context, projectPath string, mrIID int, body string) error
	PostDiscussion(ctx context.Context, projectPath string, mrIID int, body string, pos Position) error
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
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gitlab %s %s: %d %s", method, path, resp.StatusCode, string(rb))
	}
	return rb, nil
}

type mrEnvelope struct {
	IID      int    `json:"iid"`
	Title    string `json:"title"`
	WebURL   string `json:"web_url"`
	DiffRefs struct {
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
}

type changesEnvelope struct {
	Changes []FileChange `json:"changes"`
}

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
	cb, err := c.do(ctx, "GET", mrURL+"/changes", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var ch changesEnvelope
	if err := json.Unmarshal(cb, &ch); err != nil {
		return nil, nil, err
	}
	return &MR{
		IID: env.IID, Title: env.Title, WebURL: env.WebURL,
		BaseSHA: env.DiffRefs.BaseSHA, StartSHA: env.DiffRefs.StartSHA, HeadSHA: env.DiffRefs.HeadSHA,
	}, ch.Changes, nil
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
