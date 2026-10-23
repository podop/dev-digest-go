package app

import (
	"strings"
	"unicode/utf8"
)

// MaxFailureBytes caps the whole "[agent-runner] FAILED: …" line (spec 7.8.9, NFR6005: error texts
// are redacted and cut before they are printed). It leaves room for the 1,000-byte GitHub body
// the HTTP client already keeps, plus its fixed head.
const MaxFailureBytes = 1200

// failurePrefix starts the failure line on the job log.
const failurePrefix = "[agent-runner] FAILED: "

// cutMark ends a text that was cut.
const cutMark = "…"

// zeroWidth breaks a workflow-command sequence without changing how the text reads.
const zeroWidth = "​"

// FailureLine is the failure line for text (already redacted by the caller): the text is made
// safe for the job log (see LogLine) and the whole line is cut to MaxFailureBytes.
func FailureLine(text string) string { return LogLine(failurePrefix, text) }

// LogLine is prefix plus text as one job-log line that is safe to print whatever the text holds:
// workflow-command sequences are broken ("##[" anywhere is a legacy command for the Actions
// runner, and so is a leading "::"), line breaks are escaped so the text cannot start a second
// line, and the line is cut to MaxFailureBytes on a UTF-8 boundary. The prefix is trusted.
func LogLine(prefix, text string) string {
	return cutBytes(prefix+oneLine(text), MaxFailureBytes)
}

// oneLine neutralises workflow commands and escapes line breaks.
func oneLine(s string) string {
	return escapeBreaks(BreakCommands(s))
}

func escapeBreaks(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s)
}

// BreakCommands breaks every "##[" and every "::" with a zero-width space, so no run of the output
// is a workflow command however the line is split or joined.
func BreakCommands(s string) string {
	if !strings.Contains(s, "##[") && !strings.Contains(s, "::") {
		return s
	}
	s = strings.ReplaceAll(s, "##[", "#"+zeroWidth+"#[")
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		if s[i] == ':' && i+1 < len(s) && s[i+1] == ':' {
			b.WriteString(zeroWidth)
		}
	}
	return b.String()
}

// cutBytes cuts s to at most limit bytes, ending in cutMark, on a UTF-8 boundary.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	i := limit - len(cutMark)
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + cutMark
}
