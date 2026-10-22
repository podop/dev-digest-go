package mock

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"dev-digest-go/backend/internal/llm"
)

// Review is the schema name of a review run's answer.
const Review = "Review"

// skillsHeading opens the skills section of a review prompt.
const skillsHeading = "## Skills / rules\n"

// reviewFinding is a finding of the Review fixture; the field order is the order of the Node
// fixture, so the serialized answer is byte-equal to its JSON.stringify.
type reviewFinding struct {
	ID         string  `json:"id"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Title      string  `json:"title"`
	File       string  `json:"file"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Rationale  string  `json:"rationale"`
	Suggestion string  `json:"suggestion"`
	Confidence float64 `json:"confidence"`
	Kind       string  `json:"kind"`
	Skill      string  `json:"skill,omitempty"`
}

type reviewFixture struct {
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Score    int             `json:"score"`
	Findings []reviewFinding `json:"findings"`
}

// review is the fixture of the seeded PR #482 (dev-digest server/src/adapters/llm/mock.ts
// MOCK_REVIEW): both findings lie inside its diff. The first one cites skill when it is not "".
func review(skill string) reviewFixture {
	return reviewFixture{
		Verdict: "comment",
		Summary: "[mock LLM] The token-bucket limiter works, but its key trusts a client-controlled header and the bucket map never shrinks.",
		Score:   70,
		Findings: []reviewFinding{
			{
				ID: "mock-1", Severity: "WARNING", Category: "security",
				Title:      "Rate-limit key trusts the spoofable X-Forwarded-For header",
				File:       "src/middleware/ratelimit.ts",
				StartLine:  7,
				EndLine:    7,
				Rationale:  "Any client can send a fresh `X-Forwarded-For` value on every request and get a new bucket, which bypasses the limiter.",
				Suggestion: "Key on `req.ip` with Fastify's `trustProxy` configured for your load balancer.",
				Confidence: 0.9,
				Kind:       "finding",
				Skill:      skill,
			},
			{
				ID: "mock-2", Severity: "SUGGESTION", Category: "perf",
				Title:      "Bucket map grows without eviction",
				File:       "src/middleware/ratelimit.ts",
				StartLine:  4,
				EndLine:    4,
				Rationale:  "One entry is kept per distinct key forever, so memory grows with the number of clients.",
				Suggestion: "Use an LRU / TTL map, or evict buckets that have refilled completely.",
				Confidence: 0.72,
				Kind:       "finding",
			},
		},
	}
}

// reviewJSON serializes the fixture like JSON.stringify: compact, no HTML escaping.
func reviewJSON(messages []llm.Message) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(review(firstPromptSkill(messages))); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// firstPromptSkill is the name in the first "### <name>" heading of the first "## Skills / rules"
// section of the messages, or "" when the prompt carries no skills (Node firstPromptSkill). The
// section ends at the next "\n## " heading that is not a "###" one.
func firstPromptSkill(messages []llm.Message) string {
	for _, m := range messages {
		start := strings.Index(m.Content, skillsHeading)
		if start < 0 {
			continue
		}
		section := m.Content[start:]
		for from := 0; ; {
			i := strings.Index(section[from:], "\n## ")
			if i < 0 {
				break
			}
			i += from
			if after := i + len("\n## "); after < len(section) && section[after] == '#' {
				from = i + 1
				continue
			}
			section = section[:i]
			break
		}
		if name := skillHeading(section); name != "" {
			return name
		}
	}
	return ""
}

// skillHeading finds the first line starting with "### " followed by a non-space run. Lines end
// at \n, \r, U+2028 or U+2029 and the run at any JavaScript whitespace, as in the Node regexp
// /^### (\S+)/m.
func skillHeading(section string) string {
	isLineEnd := func(r rune) bool { return r == 0x0a || r == 0x0d || r == 0x2028 || r == 0x2029 }
	for lineStart := 0; lineStart <= len(section); {
		rest := section[lineStart:]
		if after, ok := strings.CutPrefix(rest, "### "); ok {
			if end := strings.IndexFunc(after, isJSSpace); end != 0 {
				if end < 0 {
					end = len(after)
				}
				return after[:end]
			}
		}
		next := strings.IndexFunc(rest, isLineEnd)
		if next < 0 {
			return ""
		}
		_, w := utf8.DecodeRuneInString(rest[next:])
		lineStart += next + w
	}
	return ""
}

// isJSSpace is the ECMAScript \s class (numeric code points: the file stays free of invisible
// characters).
func isJSSpace(r rune) bool {
	switch r {
	case 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
