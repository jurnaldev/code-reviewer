package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

var allowedSeverity = map[string]bool{"critical": true, "major": true, "minor": true, "nit": true}

func ParseFindings(s string) ([]Finding, error) {
	jsonText, ok := extractJSONObject(s)
	if !ok {
		return nil, fmt.Errorf("no JSON object found in output")
	}
	var wrapper struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(jsonText), &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal findings: %w", err)
	}
	for i, f := range wrapper.Findings {
		if !allowedSeverity[f.Severity] {
			return nil, fmt.Errorf("finding %d: invalid severity %q", i, f.Severity)
		}
	}
	return wrapper.Findings, nil
}

// extractJSONObject finds the first balanced top-level {...} substring.
// Strips fenced ```json blocks if present.
func extractJSONObject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// drop opening fence line
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
