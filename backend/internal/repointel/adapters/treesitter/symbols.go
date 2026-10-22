package treesitter

import (
	"strconv"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

// keywords are the names that are never emitted as a symbol or a reference (spec section 7.4).
var keywords = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "switch": {}, "catch": {}, "return": {}, "function": {},
	"await": {}, "typeof": {}, "new": {}, "delete": {}, "void": {}, "do": {}, "else": {}, "in": {},
	"of": {}, "instanceof": {}, "yield": {}, "super": {}, "constructor": {}, "get": {}, "set": {},
	"import": {}, "export": {}, "as": {}, "from": {}, "class": {}, "extends": {},
}

func isKeyword(name string) bool {
	_, ok := keywords[name]
	return ok
}

// symbolCollector gathers the top-level declarations of one file.
type symbolCollector struct {
	src []byte
	out []domain.Symbol
	// declLines maps a name to the line of its last recorded declaration (export back-patch).
	declLines map[string]int
}

// extractSymbols returns the symbols of the file whose syntax tree has the given root (spec
// section 7.4): top-level statements only, export unwrapping, the export-clause back-patch and
// the (name, kind, line) de-duplication.
func extractSymbols(root sitter.Node, src []byte) []domain.Symbol {
	c := &symbolCollector{src: src, declLines: map[string]int{}}
	for _, top := range children(&root) {
		node, exported := unwrapExport(top)
		c.handleDecl(node, exported)
	}
	c.backPatchExports(root)
	return dedupeSymbols(c.out)
}

// unwrapExport returns the declaration an export_statement wraps: its first child that is not
// `export`, `default`, `*`, `from`, `;`, a string or an export clause. Any other statement is
// returned as it is, not exported.
func unwrapExport(top *sitter.Node) (*sitter.Node, bool) {
	if top.Kind() != "export_statement" {
		return top, false
	}
	for _, ch := range children(top) {
		switch ch.Kind() {
		case "export", "default", "*", "from", ";", "string", "export_clause":
			continue
		}
		return ch, true
	}
	return top, true
}

func (c *symbolCollector) add(name, kind string, decl *sitter.Node, exported bool, signature string) {
	c.out = append(c.out, domain.Symbol{
		Name:      name,
		Kind:      kind,
		Line:      line(decl),
		EndLine:   endLine(decl),
		Exported:  exported,
		Signature: signature,
	})
	c.declLines[name] = line(decl)
}

// nameOf returns the text of the node's name field, "" when it has none or is a keyword.
func (c *symbolCollector) nameOf(n *sitter.Node) string {
	nameNode := field(n, "name")
	if nameNode == nil {
		return ""
	}
	name := nodeText(c.src, nameNode)
	if name == "" || isKeyword(name) {
		return ""
	}
	return name
}

func (c *symbolCollector) handleDecl(node *sitter.Node, exported bool) {
	switch node.Kind() {
	case "function_declaration", "generator_function_declaration":
		if name := c.nameOf(node); name != "" {
			c.add(name, "function", node, exported, headSignature(c.src, node))
		}
	case "class_declaration":
		name := c.nameOf(node)
		if name == "" {
			return
		}
		c.add(name, "class", node, exported, headSignature(c.src, node))
		body := field(node, "body")
		if body == nil {
			return
		}
		for _, m := range children(body) {
			if m.Kind() != "method_definition" {
				continue
			}
			mname := c.nameOf(m)
			if mname == "" {
				continue
			}
			sig := headSignature(c.src, m)
			c.add(name+"."+mname, "method", m, exported, sig)
			c.add(mname, "method", m, exported, sig)
		}
	case "interface_declaration":
		if name := c.nameOf(node); name != "" {
			c.add(name, "interface", node, exported, headSignature(c.src, node))
		}
	case "type_alias_declaration":
		if name := c.nameOf(node); name != "" {
			c.add(name, "type", node, exported, headSignature(c.src, node))
		}
	case "enum_declaration":
		if name := c.nameOf(node); name != "" {
			c.add(name, "enum", node, exported, headSignature(c.src, node))
		}
	case "lexical_declaration", "variable_declaration":
		for _, decl := range children(node) {
			if decl.Kind() != "variable_declarator" {
				continue
			}
			name := c.nameOf(decl)
			if name == "" || !isFunctionLike(field(decl, "value")) {
				continue
			}
			c.add(name, "function", decl, exported, variableSignature(c.src, decl))
		}
	}
}

// backPatchExports marks the symbols named by an `export { a, b as c }` clause (anywhere in the
// file) as exported when the symbol's line is the last recorded declaration line of its name.
func (c *symbolCollector) backPatchExports(root sitter.Node) {
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "export_statement" {
			return true
		}
		var clause *sitter.Node
		for _, ch := range children(n) {
			if ch.Kind() == "export_clause" {
				clause = ch
				break
			}
		}
		if clause == nil {
			return true
		}
		for _, spec := range children(clause) {
			if spec.Kind() != "export_specifier" {
				continue
			}
			nameNode := field(spec, "name")
			if nameNode == nil {
				continue
			}
			name := nodeText(c.src, nameNode)
			declLine, ok := c.declLines[name]
			if name == "" || !ok {
				continue
			}
			for i := range c.out {
				if c.out[i].Name == name && c.out[i].Line == declLine {
					c.out[i].Exported = true
				}
			}
		}
		return true
	})
}

// dedupeSymbols keeps the first symbol of each (name, kind, line), replaced in place by a later
// one that is exported when the kept one is not.
func dedupeSymbols(syms []domain.Symbol) []domain.Symbol {
	index := make(map[string]int, len(syms))
	out := make([]domain.Symbol, 0, len(syms))
	for _, s := range syms {
		key := s.Name + ":" + s.Kind + ":" + strconv.Itoa(s.Line)
		i, seen := index[key]
		switch {
		case !seen:
			index[key] = len(out)
			out = append(out, s)
		case s.Exported && !out[i].Exported:
			out[i] = s
		}
	}
	return out
}
