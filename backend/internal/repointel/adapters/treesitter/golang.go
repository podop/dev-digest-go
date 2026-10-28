package treesitter

import (
	"strconv"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

// extractGo returns the symbols and references of a Go file (spec FR5, FR7, FR8, FR10).
func extractGo(root sitter.Node, src []byte, g *guard) (domain.Extraction, error) {
	symbols, err := goSymbols(root, src, g)
	if err != nil {
		return domain.Extraction{}, err
	}
	refs, err := goReferences(root, src, symbols, g)
	if err != nil {
		return domain.Extraction{}, err
	}
	return domain.Extraction{Symbols: symbols, References: refs}, nil
}

// goSymbols returns the top-level declarations of a Go file: functions, methods and type
// declarations (struct → class, interface → interface, anything else → type). Package-level var
// and const declarations are not symbols.
func goSymbols(root sitter.Node, src []byte, g *guard) ([]domain.Symbol, error) {
	var out []domain.Symbol
	var err error
	add := func(nameNode *sitter.Node, kind string, decl *sitter.Node, signature string) {
		if nameNode == nil {
			return
		}
		name := nodeText(src, nameNode)
		if name == "" {
			return
		}
		first, _ := utf8.DecodeRuneInString(name)
		out = append(out, domain.Symbol{
			Name:      name,
			Kind:      kind,
			Line:      line(decl),
			EndLine:   endLine(decl),
			Exported:  unicode.IsUpper(first),
			Signature: signature,
		})
	}
	eachChild(&root, func(top *sitter.Node) bool {
		if err = g.tick(); err != nil {
			return false
		}
		switch top.Kind() {
		case "function_declaration":
			add(field(top, "name"), "function", top, headSignature(src, top))
		case "method_declaration":
			add(field(top, "name"), "method", top, headSignature(src, top))
		case "type_declaration":
			eachChild(top, func(spec *sitter.Node) bool {
				if err = g.tick(); err != nil {
					return false
				}
				switch spec.Kind() {
				case "type_spec", "type_alias":
					add(field(spec, "name"), goTypeKind(spec), spec, goTypeSignature(src, spec))
				}
				return true
			})
		}
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	return dedupeSymbols(out), nil
}

// goTypeKind is the symbol kind of a type_spec or type_alias: class for a struct, interface for
// an interface, type for anything else (aliases included).
func goTypeKind(spec *sitter.Node) string {
	if spec.Kind() == "type_spec" {
		switch t := field(spec, "type"); {
		case t == nil:
		case t.Kind() == "struct_type":
			return "class"
		case t.Kind() == "interface_type":
			return "interface"
		}
	}
	return "type"
}

// goTypeSignature is "type " plus the spec text up to the opening brace of a struct or interface
// body (the whole spec otherwise), tidied by tidyHead.
func goTypeSignature(src []byte, spec *sitter.Node) string {
	start, end := spec.StartByte(), spec.EndByte()
	if t := field(spec, "type"); t != nil {
		var body *sitter.Node
		switch t.Kind() {
		case "struct_type":
			body = firstChildKind(t, "field_declaration_list")
		case "interface_type":
			body = firstChildKind(t, "{")
		}
		if body != nil {
			end = body.StartByte()
		}
	}
	return tidyHead("type " + string(src[start:end]))
}

// goReferences returns the references of a Go file: the called name of every call (an
// identifier, or a selector's field), the identifiers and selector fields passed as call
// arguments (function values), and the type name of every composite literal. Calls are taken in
// document order, each followed by its argument names and then by the calls nested inside it. A
// (name, line) that is a declaration of the file is dropped, and each (name, line) is kept once.
func goReferences(root sitter.Node, src []byte, symbols []domain.Symbol, g *guard) ([]domain.Reference, error) {
	declared := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		declared[s.Name+":"+strconv.Itoa(s.Line)] = struct{}{}
	}
	var out []domain.Reference
	seen := map[string]struct{}{}
	add := func(name string, at *sitter.Node) {
		if name == "" {
			return
		}
		key := name + ":" + strconv.Itoa(line(at))
		if _, isDecl := declared[key]; isDecl {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, domain.Reference{Name: name, Line: line(at)})
	}

	var argErr error
	err := walk(root, g, func(n *sitter.Node) bool {
		if argErr != nil {
			return false
		}
		switch n.Kind() {
		case "call_expression":
			add(goName(src, field(n, "function")), n)
		case "argument_list":
			eachChild(n, func(arg *sitter.Node) bool {
				if argErr = g.tick(); argErr != nil {
					return false
				}
				add(goName(src, arg), arg)
				return true
			})
		case "composite_literal":
			add(goTypeName(src, field(n, "type")), n)
		}
		return true
	})
	if err == nil {
		err = argErr
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// goName is the text of an identifier or the field of a selector expression; "" for any other
// node.
func goName(src []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier":
		return nodeText(src, n)
	case "selector_expression":
		if f := field(n, "field"); f != nil {
			return nodeText(src, f)
		}
	}
	return ""
}

// goTypeName is the name of a composite literal's type: a type identifier, the name of a
// qualified type, or the base type of a generic instantiation; "" for any other type.
func goTypeName(src []byte, n *sitter.Node) string {
	if n != nil && n.Kind() == "generic_type" {
		n = field(n, "type")
	}
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "type_identifier":
		return nodeText(src, n)
	case "qualified_type":
		if name := field(n, "name"); name != nil {
			return nodeText(src, name)
		}
	}
	return ""
}

// importsGo returns the package key of a Go file and the import paths of its import declarations
// (spec FR11, FR12) in first-seen order, aliased, dot and blank imports included. A path that does
// not unquote, an empty path and a repeated path are dropped.
func importsGo(root sitter.Node, src []byte, file string, g *guard) (domain.FileImports, error) {
	out := domain.FileImports{Package: domain.GoPackageKey(file)}
	seen := map[string]struct{}{}
	var err error
	addSpec := func(spec *sitter.Node) {
		path := field(spec, "path")
		if path == nil {
			return
		}
		p, uerr := strconv.Unquote(nodeText(src, path))
		if uerr != nil || p == "" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out.Specs = append(out.Specs, p)
	}
	// visit handles one child of an import declaration or of its parenthesized list.
	var visit func(n *sitter.Node) bool
	visit = func(n *sitter.Node) bool {
		if err = g.tick(); err != nil {
			return false
		}
		switch n.Kind() {
		case "import_spec":
			addSpec(n)
		case "import_spec_list":
			eachChild(n, visit)
		}
		return err == nil
	}
	eachChild(&root, func(top *sitter.Node) bool {
		if err = g.tick(); err != nil {
			return false
		}
		if top.Kind() == "import_declaration" {
			eachChild(top, visit)
		}
		return err == nil
	})
	if err != nil {
		return domain.FileImports{}, err
	}
	return out, nil
}
