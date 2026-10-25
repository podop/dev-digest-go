// Package domain holds the rules of the workspace context: the display name, the trace
// retention setting, the export bundle and the vocabulary of stats and reset counts. It imports
// only the standard library. The Bundle types carry json tags because they are the exported file
// format itself (like the document schemas of the brief and onboarding contexts); the HTTP
// request and response bodies live in transport/http.
package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidName means the text is not an acceptable workspace display name.
var ErrInvalidName = errors.New("invalid workspace name")

// NameKey is the Settings key that stores the display name (spec D23). The lookup key
// `workspaces.name` stays "default" and never changes.
const NameKey = "workspace_name"

// DefaultName is the name shown when no valid name is stored.
const DefaultName = "default"

// MaxNameRunes is the longest accepted name, in Unicode code points (spec A10).
const MaxNameRunes = 80

// NormalizeName trims leading and trailing white space and checks the rest: 1 to MaxNameRunes
// code points, valid UTF-8, no control character (U+0000 to U+001F, U+007F). It returns the
// trimmed name, or ErrInvalidName.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) {
		return "", ErrInvalidName
	}
	n := utf8.RuneCountInString(name)
	if n < 1 || n > MaxNameRunes {
		return "", ErrInvalidName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidName
		}
	}
	return name, nil
}
