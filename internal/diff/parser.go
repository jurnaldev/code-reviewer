package diff

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type ParsedDiff struct {
	Files []FileDiff
}

type FileDiff struct {
	OldPath  string
	NewPath  string
	IsBinary bool
	IsRename bool
	Hunks    []Hunk
}

type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []DiffLine
}

type DiffLine struct {
	Kind      rune // ' ', '+', '-'
	Content   string
	NewLineNo int // 0 for deleted lines
}

func Parse(s string) (*ParsedDiff, error) {
	pd := &ParsedDiff{}
	if strings.TrimSpace(s) == "" {
		return pd, nil
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var cur *FileDiff
	var curHunk *Hunk
	var newLineCursor int

	flushFile := func() {
		if cur != nil {
			if curHunk != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
				curHunk = nil
			}
			pd.Files = append(pd.Files, *cur)
			cur = nil
		}
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			cur = &FileDiff{}
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				cur.OldPath = strings.TrimPrefix(parts[2], "a/")
				cur.NewPath = strings.TrimPrefix(parts[3], "b/")
			}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "rename from "):
			cur.IsRename = true
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.NewPath = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files "):
			cur.IsBinary = true
		case strings.HasPrefix(line, "--- "):
			p := strings.TrimPrefix(line, "--- ")
			if p != "/dev/null" {
				cur.OldPath = strings.TrimPrefix(p, "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p != "/dev/null" {
				cur.NewPath = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@"):
			if curHunk != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			curHunk = &h
			newLineCursor = h.NewStart
		case curHunk != nil:
			if len(line) == 0 {
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: ' ', Content: "", NewLineNo: newLineCursor})
				newLineCursor++
				continue
			}
			kind := rune(line[0])
			content := line[1:]
			switch kind {
			case ' ':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: ' ', Content: content, NewLineNo: newLineCursor})
				newLineCursor++
			case '+':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: '+', Content: content, NewLineNo: newLineCursor})
				newLineCursor++
			case '-':
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: '-', Content: content, NewLineNo: 0})
			default:
				// header lines like "index ..." land here when no hunk; skip
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flushFile()
	return pd, nil
}

func parseHunkHeader(line string) (Hunk, error) {
	// @@ -A,B +C,D @@ optional context
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) < 3 {
		return Hunk{}, fmt.Errorf("bad hunk header %q", line)
	}
	body := strings.TrimSpace(parts[1])
	tokens := strings.Fields(body)
	var h Hunk
	for _, t := range tokens {
		switch {
		case strings.HasPrefix(t, "-"):
			s, l, err := parseRange(strings.TrimPrefix(t, "-"))
			if err != nil {
				return Hunk{}, err
			}
			h.OldStart, h.OldLines = s, l
		case strings.HasPrefix(t, "+"):
			s, l, err := parseRange(strings.TrimPrefix(t, "+"))
			if err != nil {
				return Hunk{}, err
			}
			h.NewStart, h.NewLines = s, l
		}
	}
	return h, nil
}

func parseRange(s string) (start, lines int, err error) {
	parts := strings.SplitN(s, ",", 2)
	start, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	lines = 1
	if len(parts) == 2 {
		lines, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}
	return start, lines, nil
}
