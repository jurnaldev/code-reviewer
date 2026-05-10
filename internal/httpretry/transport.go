package httpretry

import (
	"net/http"
	"strconv"
	"time"
)

// Transport retries 429 and 5xx responses with exponential backoff,
// honoring the Retry-After header when present.
type Transport struct {
	Inner http.RoundTripper
	Max   int           // max retries (excluding the initial attempt)
	Base  time.Duration // base backoff (doubles each retry)
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}

	var lastResp *http.Response
	var lastErr error
	delay := t.Base
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}

	for attempt := 0; attempt <= t.Max; attempt++ {
		// Reset body for retries when GetBody is set (set automatically by NewRequest
		// for *bytes.Buffer / *bytes.Reader / *strings.Reader bodies).
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := inner.RoundTrip(req)
		if err != nil {
			lastErr = err
			if !sleep(req, delay) {
				return nil, err
			}
			delay *= 2
			continue
		}

		if !shouldRetry(resp.StatusCode) {
			return resp, nil
		}

		// Retryable status. If we have retries left, drain + close + sleep.
		lastResp = resp
		if attempt == t.Max {
			return resp, nil
		}
		wait := delay
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		_ = resp.Body.Close()
		if !sleep(req, wait) {
			return resp, nil
		}
		delay *= 2
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func shouldRetry(code int) bool {
	return code == 429 || (code >= 500 && code <= 599)
}

func sleep(req *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-req.Context().Done():
		return false
	}
}
