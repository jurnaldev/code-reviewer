package gitlab

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type MRRef struct {
	BaseURL     string
	ProjectPath string
	MRIID       int
}

func ParseURL(raw string) (*MRRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	idx := -1
	for i, p := range parts {
		if p == "-" && i+2 < len(parts) && parts[i+1] == "merge_requests" {
			idx = i
			break
		}
	}
	if idx < 1 {
		return nil, fmt.Errorf("not a merge request URL: %s", raw)
	}
	iid, err := strconv.Atoi(parts[idx+2])
	if err != nil {
		return nil, fmt.Errorf("bad MR IID: %v", err)
	}
	return &MRRef{
		BaseURL:     u.Scheme + "://" + u.Host,
		ProjectPath: strings.Join(parts[:idx], "/"),
		MRIID:       iid,
	}, nil
}
