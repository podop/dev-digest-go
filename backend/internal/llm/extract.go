package llm

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf16"
)

var fenceRE = regexp.MustCompile("(?is)```(?:json)?\\s*(.*?)```")

// UTF16Len is the length of s in UTF-16 code units: what JavaScript calls its length, and what
// the Node backend's text limits, token estimates and log sizes count.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// truncateUTF16 cuts s to at most max UTF-16 units and marks the cut with "…".
func truncateUTF16(s string, max int) string {
	if UTF16Len(s) <= max {
		return s
	}
	n := 0
	for i, r := range s {
		w := len(utf16.AppendRune(nil, r))
		if n+w > max {
			return s[:i] + "…"
		}
		n += w
	}
	return s
}

// extractJSON returns the JSON text in a model answer: the trimmed text itself when it is
// valid JSON, else the first fenced block, else the first balanced {…} or […] (spec FR209). On
// failure it returns the parse error of the last candidate.
func extractJSON(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	cand := candidate(trimmed)
	var v any
	if err := json.Unmarshal([]byte(cand), &v); err != nil {
		return "", err
	}
	return cand, nil
}

func candidate(trimmed string) string {
	if m := fenceRE.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1])
	}
	start := strings.IndexAny(trimmed, "{[")
	if start < 0 {
		return trimmed
	}
	open, closer := trimmed[start], byte('}')
	if open == '[' {
		closer = ']'
	}
	depth, inString, escaped := 0, false, false
	for i := start; i < len(trimmed); i++ {
		c := trimmed[i]
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == open:
			depth++
		case c == closer:
			depth--
			if depth == 0 {
				return trimmed[start : i+1]
			}
		}
	}
	return trimmed[start:]
}
