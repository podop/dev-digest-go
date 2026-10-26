package app

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"unicode/utf8"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/logging"
)

// maxMessage bounds a message shown to the user, in bytes.
const maxMessage = 300

// scrub removes every secret from text shown to the user (spec NFR1): each whole secret (also in
// its JSON-escaped and quoted forms), then token-shaped runs (gitlab.TokenShape), then cuts the text to maxMessage
// bytes (redact first, cut second: a cut can leave half a secret). The messages of a test are
// assembled here from fixed text and stored fields, never from GitLab's answer, so the fragment
// pass of custom.Scrub, which also masks ordinary words, is not needed.
func scrub(msg string, secrets ...string) string {
	msg = custom.ScrubWhole(msg, secrets, logging.Redacted)
	msg = gitlab.TokenShape.ReplaceAllString(msg, logging.Redacted)
	return truncate(msg, maxMessage)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// newID returns a random version 4 UUID in its canonical lower-case form.
func newID() string {
	var b [16]byte
	rand.Read(b[:]) // never fails since Go 1.24: it crashes the program instead
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var idShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validID reports whether s is a canonical lower-case UUID. Anything else cannot be the id of a
// connection: it is answered 404 without reaching the database (a uuid column refuses it) or the
// secrets file.
func validID(s string) bool { return idShape.MatchString(s) }
