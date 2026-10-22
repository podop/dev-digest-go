package treesitter

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

// isJSSpace reports whether r is in ECMAScript's \s set (WhiteSpace and LineTerminator). It is
// not unicode.IsSpace: U+FEFF is in the set and U+0085 is not.
func isJSSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0D, r == 0x20, r == 0xA0, r == 0x1680,
		r >= 0x2000 && r <= 0x200A, r == 0x2028, r == 0x2029, r == 0x202F, r == 0x205F,
		r == 0x3000, r == 0xFEFF:
		return true
	}
	return false
}

// collapseSpace replaces every run of ECMAScript whitespace with one space and trims the ends,
// like JavaScript's s.replace(/\s+/g, ' ').trim().
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if isJSSpace(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tidyHead finishes a signature from the declaration head: whitespace collapsed, one trailing
// "=>", "{" or "=" (with the whitespace before it) removed, and the result cut to
// domain.MaxSignatureUnits UTF-16 units (119 units plus an ellipsis when longer).
func tidyHead(head string) string {
	head = collapseSpace(head)
	switch {
	case strings.HasSuffix(head, "=>"):
		head = head[:len(head)-2]
	case strings.HasSuffix(head, "{"), strings.HasSuffix(head, "="):
		head = head[:len(head)-1]
	}
	head = strings.TrimRightFunc(head, isJSSpace)
	if domain.UTF16Len(head) > domain.MaxSignatureUnits {
		// JavaScript's slice(0, 119) can end inside a surrogate pair, which UTF-8 cannot hold;
		// the straddling character is dropped whole instead.
		head = domain.ClampUTF16(head, domain.MaxSignatureUnits-1) + "…"
	}
	return head
}

// headSignature is the node text up to the start of its body field (the whole text when it has
// none), tidied by tidyHead.
func headSignature(src []byte, n *sitter.Node) string {
	start, end := n.StartByte(), n.EndByte()
	if body := field(n, "body"); body != nil {
		if b := body.StartByte(); b > start && b <= end {
			end = b
		}
	}
	return tidyHead(string(src[start:end]))
}

// isFunctionLike reports whether n is the value of a function-style variable declarator.
func isFunctionLike(n *sitter.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind() {
	case "arrow_function", "function_expression", "generator_function":
		return true
	}
	return false
}

// variableSignature builds the signature of a `const f = (x) => …` declarator: the enclosing
// declaration text (including const/let/var) up to the start of the function value's body.
func variableSignature(src []byte, decl *sitter.Node) string {
	base := decl
	if p := decl.Parent(); p != nil {
		if k := p.Kind(); k == "lexical_declaration" || k == "variable_declaration" {
			base = p
		}
	}
	if value := field(decl, "value"); isFunctionLike(value) {
		if body := field(value, "body"); body != nil {
			if b := body.StartByte(); b > base.StartByte() {
				return tidyHead(string(src[base.StartByte():b]))
			}
		}
	}
	return headSignature(src, base)
}
