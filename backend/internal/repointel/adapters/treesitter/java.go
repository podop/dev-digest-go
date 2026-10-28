package treesitter

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

// extractJava returns the symbols and references of a Java file (spec FR6, FR7, FR9, FR10).
func extractJava(root sitter.Node, src []byte, g *guard) (domain.Extraction, error) {
	symbols, err := javaSymbols(root, src, g)
	if err != nil {
		return domain.Extraction{}, err
	}
	refs, err := javaReferences(root, src, symbols, g)
	if err != nil {
		return domain.Extraction{}, err
	}
	return domain.Extraction{Symbols: symbols, References: refs}, nil
}

// javaSymbolKinds maps the declaration node kinds that are symbols to their symbol kind.
var javaSymbolKinds = map[string]string{
	"class_declaration":           "class",
	"record_declaration":          "class",
	"interface_declaration":       "interface",
	"annotation_type_declaration": "interface",
	"enum_declaration":            "enum",
	"method_declaration":          "method",
	"constructor_declaration":     "method",
}

// javaSymbols returns the type, method and constructor declarations of a Java file at any depth
// (members of nested, inner, local and anonymous types included) in document order. A
// declaration is exported when its modifiers hold public or when it is a direct member of an
// interface or annotation type body (implicitly public).
func javaSymbols(root sitter.Node, src []byte, g *guard) ([]domain.Symbol, error) {
	var out []domain.Symbol
	// members holds the node ids of the direct children of interface and annotation type
	// bodies; walk reaches a body before its members, so a member is marked when it is visited.
	members := map[uintptr]struct{}{}
	var bodyErr error
	err := walk(root, g, func(n *sitter.Node) bool {
		if bodyErr != nil {
			return false
		}
		switch k := n.Kind(); k {
		case "interface_body", "annotation_type_body":
			eachChild(n, func(c *sitter.Node) bool {
				if bodyErr = g.tick(); bodyErr != nil {
					return false
				}
				members[c.Id()] = struct{}{}
				return true
			})
		default:
			kind, ok := javaSymbolKinds[k]
			if !ok {
				break
			}
			nameNode := field(n, "name")
			if nameNode == nil {
				break
			}
			name := nodeText(src, nameNode)
			if name == "" {
				break
			}
			var public bool
			var headStart uint
			if public, headStart, bodyErr = javaModifiers(n, g); bodyErr != nil {
				return false
			}
			_, member := members[n.Id()]
			out = append(out, domain.Symbol{
				Name:      name,
				Kind:      kind,
				Line:      line(n),
				EndLine:   endLine(n),
				Exported:  member || public,
				Signature: javaSignature(src, n, headStart),
			})
		}
		return true
	})
	if err == nil {
		err = bodyErr
	}
	if err != nil {
		return nil, err
	}
	return dedupeSymbols(out), nil
}

// javaModifiers reads the modifiers of a declaration: whether they hold public, and the byte the
// signature starts at — the first modifier that is not an annotation (or a comment), the end of
// the modifiers when they are all annotations, the start of decl when it has none.
func javaModifiers(decl *sitter.Node, g *guard) (public bool, headStart uint, err error) {
	headStart = decl.StartByte()
	modifiers := firstChildKind(decl, "modifiers")
	if modifiers == nil {
		return false, headStart, nil
	}
	headStart = modifiers.EndByte()
	leading := true
	eachChild(modifiers, func(c *sitter.Node) bool {
		if err = g.tick(); err != nil {
			return false
		}
		switch k := c.Kind(); {
		case k == "public":
			public = true
		case k == "annotation", k == "marker_annotation", k == "line_comment", k == "block_comment":
			return true
		}
		if leading {
			headStart, leading = c.StartByte(), false
		}
		return true
	})
	return public, headStart, err
}

// javaSignature is the declaration head from start up to its body (the whole declaration without
// its closing ";" when it has none), tidied by tidyHead. With the start of javaModifiers,
// `@GetMapping("/x") public Order get(long id) {` gives `public Order get(long id)`.
func javaSignature(src []byte, decl *sitter.Node, start uint) string {
	end := decl.EndByte()
	if body := field(decl, "body"); body != nil {
		if b := body.StartByte(); b >= start && b <= end {
			end = b
		}
	}
	head := strings.TrimRightFunc(string(src[start:end]), isJSSpace)
	return tidyHead(strings.TrimSuffix(head, ";"))
}

// javaReferences returns the references of a Java file: the name of every method invocation,
// then the type name of every object creation, then the name of every annotation, each in
// document order (the grouping of the TypeScript extractor). A (name, line) that is a
// declaration of the file is dropped, and each (name, line) is kept once.
func javaReferences(root sitter.Node, src []byte, symbols []domain.Symbol, g *guard) ([]domain.Reference, error) {
	var calls, news, annotations []domain.Reference
	var typeErr error
	err := walk(root, g, func(n *sitter.Node) bool {
		if typeErr != nil {
			return false
		}
		switch n.Kind() {
		case "method_invocation":
			if name := field(n, "name"); name != nil {
				calls = append(calls, domain.Reference{Name: nodeText(src, name), Line: line(n)})
			}
		case "object_creation_expression":
			var name string
			name, typeErr = javaTypeName(src, field(n, "type"), g)
			if name != "" {
				news = append(news, domain.Reference{Name: name, Line: line(n)})
			}
		case "annotation", "marker_annotation":
			if name := javaLastIdentifier(src, field(n, "name")); name != "" {
				annotations = append(annotations, domain.Reference{Name: name, Line: line(n)})
			}
		}
		return true
	})
	if err == nil {
		err = typeErr
	}
	if err != nil {
		return nil, err
	}
	refs := newRefFilter(symbols)
	for _, list := range [][]domain.Reference{calls, news, annotations} {
		for _, r := range list {
			if r.Name != "" {
				refs.add(r)
			}
		}
	}
	return refs.out, nil
}

// javaTypeName is the simple name of an object creation's type: a type identifier, the last
// identifier of a scoped type (`java.util.ArrayList` → ArrayList), or that of a generic type's
// base (`Box<String>` → Box); "" for any other node.
func javaTypeName(src []byte, n *sitter.Node, g *guard) (string, error) {
	if n != nil && n.Kind() == "generic_type" {
		n = firstChildKind(n, "scoped_type_identifier", "type_identifier")
	}
	if n == nil {
		return "", nil
	}
	switch n.Kind() {
	case "type_identifier":
		return nodeText(src, n), nil
	case "scoped_type_identifier":
		var last *sitter.Node
		var err error
		eachChild(n, func(c *sitter.Node) bool {
			if err = g.tick(); err != nil {
				return false
			}
			if c.Kind() == "type_identifier" {
				last = c
			}
			return true
		})
		if err != nil || last == nil {
			return "", err
		}
		return nodeText(src, last), nil
	}
	return "", nil
}

// javaLastIdentifier is the text of an identifier or the name of a scoped identifier
// (`org.junit.Test` → Test); "" for any other node.
func javaLastIdentifier(src []byte, n *sitter.Node) string {
	if n != nil && n.Kind() == "scoped_identifier" {
		n = field(n, "name")
	}
	if n == nil || n.Kind() != "identifier" {
		return ""
	}
	return nodeText(src, n)
}

// importsJava returns the package key of a Java file and its imports (spec FR11, FR13) in
// first-seen order: `import a.b.C;` → a.b.C, `import a.b.*;` → a.b.*, a static import gets the
// prefix "static:". Names are rebuilt from their identifiers, so white space and comments inside
// them do not count. An empty name and a repeated spec are dropped. A file without a package
// declaration has the key "java:".
func importsJava(root sitter.Node, src []byte, g *guard) (domain.FileImports, error) {
	var pkg string
	var specs []string
	seen := map[string]struct{}{}
	var err error
	eachChild(&root, func(top *sitter.Node) bool {
		if err = g.tick(); err != nil {
			return false
		}
		switch top.Kind() {
		case "package_declaration":
			if pkg != "" {
				break
			}
			pkg, err = javaQualifiedName(src, firstChildKind(top, "scoped_identifier", "identifier"), g)
		case "import_declaration":
			var spec string
			spec, err = javaQualifiedName(src, firstChildKind(top, "scoped_identifier", "identifier"), g)
			if err != nil || spec == "" {
				break
			}
			if hasChildKind(top, "asterisk") {
				spec += ".*"
			}
			if hasChildKind(top, "static") {
				spec = "static:" + spec
			}
			if _, dup := seen[spec]; !dup {
				seen[spec] = struct{}{}
				specs = append(specs, spec)
			}
		}
		return err == nil
	})
	if err != nil {
		return domain.FileImports{}, err
	}
	return domain.FileImports{Package: domain.JavaPackageKey(pkg), Specs: specs}, nil
}

// javaQualifiedName joins the identifiers of a name node (an identifier or a scoped identifier)
// with "."; "" for nil.
func javaQualifiedName(src []byte, n *sitter.Node, g *guard) (string, error) {
	if n == nil {
		return "", nil
	}
	var parts []string
	err := walk(*n, g, func(c *sitter.Node) bool {
		if c.Kind() == "identifier" {
			parts = append(parts, nodeText(src, c))
		}
		return true
	})
	if err != nil {
		return "", err
	}
	return strings.Join(parts, "."), nil
}
