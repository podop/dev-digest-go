package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// kebabPattern is the kebab-case rule of export names and skill names.
var kebabPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// exportPrefix starts the default export name.
const exportPrefix = "devdigest-"

// UTF16Len is the length of s in UTF-16 code units, which is how the contracts count characters.
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

// NormalizeName trims a plugin name and checks the rest by the workspace-name rule (D5): valid
// UTF-8, 1 to PluginNameMax code points, no control character (U+0000 to U+001F, U+007F). It
// returns the trimmed name or ErrInvalidName.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) {
		return "", ErrInvalidName
	}
	n := utf8.RuneCountInString(name)
	if n < 1 || n > PluginNameMax {
		return "", ErrInvalidName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidName
		}
	}
	return name, nil
}

// CheckFileName checks the upload's file name (A5): valid UTF-8, 1 to FileNameMax code points,
// no control character (C0, DEL, C1). The name is not trimmed.
func CheckFileName(name string) error {
	if !utf8.ValidString(name) {
		return ErrInvalidFileName
	}
	n := utf8.RuneCountInString(name)
	if n < 1 || n > FileNameMax {
		return ErrInvalidFileName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrInvalidFileName
		}
	}
	return nil
}

// ValidExportName reports whether name is an export name: 1 to ExportNameMax characters of
// kebab-case (lower-case letters, digits, single dashes).
func ValidExportName(name string) bool {
	return len(name) >= 1 && len(name) <= ExportNameMax && kebabPattern.MatchString(name)
}

// CheckExportName returns ErrInvalidExportName unless ValidExportName(name).
func CheckExportName(name string) error {
	if !ValidExportName(name) {
		return ErrInvalidExportName
	}
	return nil
}

// DefaultName is the export name when the request has none (A9): "devdigest-" and the
// workspace name as a slug (lower-cased, every run of characters outside a-z and 0-9 a single
// "-", trimmed of "-"; "workspace" when nothing is left), cut so that the whole is at most
// ExportNameMax characters.
func DefaultName(workspaceName string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(workspaceName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if room := ExportNameMax - len(exportPrefix); len(slug) > room {
		slug = strings.TrimRight(slug[:room], "-")
	}
	if slug == "" {
		slug = "workspace"
	}
	return exportPrefix + slug
}

// ExportFileName is "<name>-<YYYY-MM-DD>.zip", the date being now in UTC. Characters outside
// A-Z a-z 0-9 . _ - in name become "-", so the result is safe in a Content-Disposition header.
func ExportFileName(name string, now time.Time) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return fmt.Sprintf("%s-%s.zip", b.String(), now.UTC().Format("2006-01-02"))
}

// UniqueNames returns names made unique, in order: the first holder of a name keeps it and a
// later one becomes "<name> (2)", "<name> (3)", taking the next suffix not yet used by any
// earlier result (the workspace export's rule, spec FR4). The input is in creation order and is
// not changed.
func UniqueNames(names []string) []string {
	used := make(map[string]struct{}, len(names))
	out := make([]string, len(names))
	for i, name := range names {
		candidate := name
		for n := 2; ; n++ {
			if _, taken := used[candidate]; !taken {
				break
			}
			candidate = fmt.Sprintf("%s (%d)", name, n)
		}
		used[candidate] = struct{}{}
		out[i] = candidate
	}
	return out
}

// ConfirmRemoval is the check of DELETE /plugins/:id: confirm must equal the plugin's name
// exactly (case-sensitive, untrimmed), else ErrConfirmationMismatch.
func ConfirmRemoval(name, confirm string) error {
	if name != confirm {
		return ErrConfirmationMismatch
	}
	return nil
}
