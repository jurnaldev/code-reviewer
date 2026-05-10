package mem9

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
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	AgentID string
	HTTP    *http.Client
	Timeout time.Duration
}

type Client struct {
	cfg Config
}

func New(c Config) *Client {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return &Client{cfg: c}
}

func (c *Client) Create(ctx context.Context, in CreateInput) (string, error) {
	body := map[string]any{
		"content":  in.Content,
		"tags":     in.Tags,
		"metadata": in.Metadata,
	}
	buf, _ := json.Marshal(body)
	req, err := c.req(ctx, "POST", "/v1alpha2/mem9s/memories", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	rb, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", fmt.Errorf("decode create: %w", err)
	}
	return out.ID, nil
}

func (c *Client) Update(ctx context.Context, id string, in CreateInput) error {
	body := map[string]any{
		"content":  in.Content,
		"tags":     in.Tags,
		"metadata": in.Metadata,
	}
	buf, _ := json.Marshal(body)
	req, err := c.req(ctx, "PUT", "/v1alpha2/mem9s/memories/"+url.PathEscape(id), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	_, err = c.do(req)
	return err
}

func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := c.req(ctx, "DELETE", "/v1alpha2/mem9s/memories/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	_, err = c.do(req)
	return err
}

func (c *Client) Search(ctx context.Context, in SearchInput) ([]Hit, error) {
	q := url.Values{}
	if in.Query != "" {
		q.Set("q", in.Query)
	}
	if len(in.Tags) > 0 {
		q.Set("tags", strings.Join(in.Tags, ","))
	}
	if in.Mode != "" {
		q.Set("mode", in.Mode)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	if in.Offset > 0 {
		q.Set("offset", strconv.Itoa(in.Offset))
	}
	path := "/v1alpha2/mem9s/memories"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	req, err := c.req(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	rb, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Memories []Hit `json:"memories"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return out.Memories, nil
}

func (c *Client) req(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-API-Key", c.cfg.APIKey)
	if c.cfg.AgentID != "" {
		r.Header.Set("X-Mnemo-Agent-Id", c.cfg.AgentID)
	}
	return r, nil
}

func (c *Client) do(r *http.Request) ([]byte, error) {
	resp, err := c.cfg.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mem9 %s %s: %d %s", r.Method, r.URL.Path, resp.StatusCode, string(rb))
	}
	return rb, nil
}
