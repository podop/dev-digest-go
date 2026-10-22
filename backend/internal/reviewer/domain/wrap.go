package domain

import (
	"regexp"
	"strings"
)

// untrustedTag matches any opening or closing "untrusted" tag the way the Node pattern
// /<(\s*\/?\s*untrusted\b[^>]*>)/gi does: tolerant of case, inner whitespace and attributes
// ("</UNTRUSTED>", "< /untrusted >", "<untrusted foo>"). Go's \s is ASCII only and its (?i) folds
// U+017F into "s", so the whitespace class and the letters are spelled out.
var untrustedTag = regexp.MustCompile(
	`<([` + jsSpace + `]*/?[` + jsSpace + `]*[uU][nN][tT][rR][uU][sS][tT][eE][dD]\b[^>]*>)`)

// WrapUntrusted wraps content in the delimiter block the prompt uses for everything an outsider
// controls: `<untrusted source="<label>">`, the content, `</untrusted>`. Any opening or closing
// "untrusted" tag inside the content gets a backslash after "<" ("</untrusted>" becomes
// "<\/untrusted>"), so only the wrapper's own tags remain real delimiters. The label is written
// as given; escape an outsider-controlled one with ContextLabel first.
func WrapUntrusted(label, content string) string {
	safe := untrustedTag.ReplaceAllString(content, `<\${1}`)
	return `<untrusted source="` + label + "\">\n" + safe + "\n</untrusted>"
}

var labelEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

// ContextLabel escapes a repo path for use as a delimiter label: & " < > become entities, so a
// path cannot close the attribute or the tag.
func ContextLabel(path string) string {
	return labelEscaper.Replace(path)
}
