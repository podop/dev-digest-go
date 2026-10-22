package treesitter

import (
	"context"
	"fmt"
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// readChunk bounds the bytes handed to the parser per read: the binding copies every chunk it is
// given into C memory, so offering the whole remainder each time would be quadratic.
const readChunk = 32 << 10

// Analyzer implements app.Analyzer with the tree-sitter grammars. It holds no state: every call
// creates its own Parser (the binding has no finalizers, so the parser and the tree are closed
// before the call returns) and is therefore safe for concurrent use.
type Analyzer struct{}

var _ app.Analyzer = (*Analyzer)(nil)

// New returns an Analyzer.
func New() *Analyzer { return &Analyzer{} }

// Extract returns the symbols and references of one file (spec section 7.4). A file with syntax
// errors yields what the parsable parts of its error-recovered tree hold.
func (a *Analyzer) Extract(ctx context.Context, file, text string) (domain.Extraction, error) {
	var out domain.Extraction
	err := withTree(ctx, file, text, func(root sitter.Node, src []byte, syntax domain.Syntax) {
		out.Symbols = extractSymbols(root, src)
		out.References = extractReferences(root, src, syntax, out.Symbols)
	})
	if err != nil {
		return domain.Extraction{}, err
	}
	return out, nil
}

// Imports returns the import specifiers of one file in first-seen order (spec section 7.5).
func (a *Analyzer) Imports(ctx context.Context, file, text string) ([]string, error) {
	var out []string
	err := withTree(ctx, file, text, func(root sitter.Node, src []byte, _ domain.Syntax) {
		out = extractImports(root, src, isTypeScriptPath(file))
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// withTree parses text with the grammar of file's extension and calls fn while the tree is
// alive; the nodes passed to fn must not be used after it returns. The parse stops when ctx ends
// (checked by the parser's progress callback) and the error then wraps ctx.Err().
func withTree(ctx context.Context, file, text string, fn func(root sitter.Node, src []byte, syntax domain.Syntax)) error {
	syntax, ok := domain.SyntaxForPath(file)
	if !ok {
		return fmt.Errorf("parse %q: unsupported extension", file)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	lang, err := Language(syntax)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return fmt.Errorf("parse: set %s language: %w", syntax, err)
	}
	src := []byte(text)
	tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
		if offset >= len(src) {
			return nil
		}
		return src[offset:min(offset+readChunk, len(src))]
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool {
		return ctx.Err() != nil
	}})
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("parse: %w", err)
		}
		return fmt.Errorf("parse %s: no syntax tree", syntax)
	}
	defer tree.Close()
	fn(*tree.RootNode(), src, syntax)
	return nil
}

// isTypeScriptPath reports whether p is a .ts or .tsx file: the files whose imports TypeScript
// elides when they are never used as values (.jsx follows the JavaScript rules).
func isTypeScriptPath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".ts", ".tsx":
		return true
	}
	return false
}

// nodeText returns the source text of n.
func nodeText(src []byte, n *sitter.Node) string {
	return string(src[n.StartByte():n.EndByte()])
}

// field returns the child of n with the field name, or nil.
func field(n *sitter.Node, name string) *sitter.Node {
	if n == nil {
		return nil
	}
	return n.ChildByFieldName(name)
}

// children returns all children of n (named and anonymous) in order.
func children(n *sitter.Node) []*sitter.Node {
	count := n.ChildCount()
	out := make([]*sitter.Node, 0, count)
	for i := range count {
		if c := n.Child(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// line returns the 1-based first line of n.
func line(n *sitter.Node) int { return int(n.StartPosition().Row) + 1 }

// endLine returns the 1-based last line of n.
func endLine(n *sitter.Node) int { return int(n.EndPosition().Row) + 1 }

// walk visits n and all its descendants in document order. visit returns false to skip the
// subtree of the node it was given.
func walk(root sitter.Node, visit func(n *sitter.Node) bool) {
	cur := root.Walk()
	defer cur.Close()
	for {
		if visit(cur.Node()) && cur.GotoFirstChild() {
			continue
		}
		for !cur.GotoNextSibling() {
			if !cur.GotoParent() {
				return
			}
		}
	}
}
