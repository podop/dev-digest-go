package domain

import (
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ErrorText is the stored form of a failure message of a case or a run: provider keys are
// redacted first (cutting first could leave half a key unredacted), every run of JavaScript
// whitespace becomes one space, the ends are trimmed, an empty message is UnknownError, and the
// text is cut to ErrorTextMax UTF-16 units. redact may be nil (no redaction).
func ErrorText(raw string, redact func(string) string) string {
	if redact != nil {
		raw = redact(raw)
	}
	var sb strings.Builder
	inSpace := false
	for _, r := range raw {
		if isJSSpace(r) {
			inSpace = true
			continue
		}
		if inSpace {
			sb.WriteByte(' ')
			inSpace = false
		}
		sb.WriteRune(r)
	}
	// A leading run was written as one space and a trailing run not at all; the trim removes the first.
	msg := reviewer.JSTrim(sb.String())
	if msg == "" {
		msg = UnknownError
	}
	return reviewer.PrefixUnits(msg, ErrorTextMax)
}

// isJSSpace is JavaScript's \s: WhiteSpace and LineTerminator.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
