package treesitter

import (
	"strconv"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

// leftmostName is the leftmost identifier of a name node (an identifier, or the object of a member
// expression); "" when there is none.
func leftmostName(src []byte, n *sitter.Node) string {
	for n != nil {
		switch n.Kind() {
		case "identifier", "type_identifier":
			return nodeText(src, n)
		case "member_expression":
			n = field(n, "object")
		default:
			return ""
		}
	}
	return ""
}

// rightmostName is the name a member expression ends in (its property), or the text of a plain
// identifier; "" when there is none.
func rightmostName(src []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier", "type_identifier", "property_identifier":
		return nodeText(src, n)
	case "member_expression":
		if prop := field(n, "property"); prop != nil {
			return nodeText(src, prop)
		}
	}
	return ""
}

// extractReferences returns the call, `new` and (TSX grammar only) JSX element references of a
// file (spec section 7.4): nothing inside an import statement, no keyword, no (name, line) that
// is a declaration of the same file (symbols), de-duplicated by (name, line). Like the Node
// extractor it lists calls first, then `new` expressions, then JSX opening and self-closing
// elements, each in document order.
func extractReferences(root sitter.Node, src []byte, syntax domain.Syntax, symbols []domain.Symbol, g *guard) ([]domain.Reference, error) {
	var calls, news, opening, selfClosing []domain.Reference
	err := walk(root, g, func(n *sitter.Node) bool {
		switch n.Kind() {
		case "import_statement":
			return false
		case "call_expression":
			fn := field(n, "function")
			if fn == nil {
				break
			}
			switch fn.Kind() {
			case "identifier":
				calls = append(calls, domain.Reference{Name: nodeText(src, fn), Line: line(n)})
			case "member_expression":
				if name := rightmostName(src, fn); name != "" {
					calls = append(calls, domain.Reference{Name: name, Line: line(n)})
				}
			}
		case "new_expression":
			if name := rightmostName(src, field(n, "constructor")); name != "" {
				news = append(news, domain.Reference{Name: name, Line: line(n)})
			}
		case "jsx_opening_element", "jsx_self_closing_element":
			if syntax != domain.SyntaxTSX {
				break
			}
			name := leftmostName(src, field(n, "name"))
			if name == "" || ('a' <= name[0] && name[0] <= 'z') {
				break
			}
			ref := domain.Reference{Name: name, Line: line(n)}
			if n.Kind() == "jsx_opening_element" {
				opening = append(opening, ref)
			} else {
				selfClosing = append(selfClosing, ref)
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	refs := newRefFilter(symbols)
	for _, list := range [][]domain.Reference{calls, news, opening, selfClosing} {
		for _, r := range list {
			if !isKeyword(r.Name) {
				refs.add(r)
			}
		}
	}
	return refs.out, nil
}

// refFilter collects the references of one file under the dedupe rules every language shares
// (spec section 7.4, FR10): a (name, line) that is a declaration of the file is dropped, and each
// (name, line) is kept once, the first time it is added.
type refFilter struct {
	declared map[string]struct{}
	seen     map[string]struct{}
	// out holds the kept references in the order they were added; nil when none was kept.
	out []domain.Reference
}

// newRefFilter returns a refFilter that drops the (name, line) of every symbol.
func newRefFilter(symbols []domain.Symbol) *refFilter {
	declared := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		declared[refKey(s.Name, s.Line)] = struct{}{}
	}
	return &refFilter{declared: declared, seen: map[string]struct{}{}}
}

// add keeps r unless it is a declaration of the file or was kept before.
func (f *refFilter) add(r domain.Reference) {
	key := refKey(r.Name, r.Line)
	if _, isDecl := f.declared[key]; isDecl {
		return
	}
	if _, dup := f.seen[key]; dup {
		return
	}
	f.seen[key] = struct{}{}
	f.out = append(f.out, r)
}

func refKey(name string, line int) string { return name + ":" + strconv.Itoa(line) }
