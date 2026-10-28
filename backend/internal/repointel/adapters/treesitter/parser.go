package treesitter

// #include <stdlib.h>
import "C"

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// readChunk bounds the bytes handed to the parser per read: the binding copies every chunk it is
// given into C memory, so offering the whole remainder each time would be quadratic.
const readChunk = 32 << 10

// checkEvery is how many visited nodes pass between two looks at the context (see guard).
const checkEvery = 1024

// guard makes the extraction passes honour the context: they call tick for every node they
// visit and stop with its error once the context is done, so a hostile file cannot keep a worker
// busy past the caller's deadline after the parse itself has finished.
type guard struct {
	ctx context.Context
	n   int
}

// tick counts one visited node and, every checkEvery nodes, returns an error wrapping the
// context's error when the context is done.
func (g *guard) tick() error {
	g.n++
	if g.n%checkEvery != 0 {
		return nil
	}
	if err := g.ctx.Err(); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	return nil
}

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
	err := withTree(ctx, file, text, func(root sitter.Node, src []byte, syntax domain.Syntax, g *guard) error {
		var err error
		switch syntax {
		case domain.SyntaxGo:
			out, err = extractGo(root, src, g)
			return err
		case domain.SyntaxJava:
			out, err = extractJava(root, src, g)
			return err
		}
		if out.Symbols, err = extractSymbols(root, src, g); err != nil {
			return err
		}
		out.References, err = extractReferences(root, src, syntax, out.Symbols, g)
		return err
	})
	if err != nil {
		return domain.Extraction{}, err
	}
	return out, nil
}

// Imports returns the package key of one file and its import specifiers in first-seen order
// (spec section 7.5, FR11). TypeScript and JavaScript files have no package key.
func (a *Analyzer) Imports(ctx context.Context, file, text string) (domain.FileImports, error) {
	var out domain.FileImports
	err := withTree(ctx, file, text, func(root sitter.Node, src []byte, syntax domain.Syntax, g *guard) error {
		var err error
		switch syntax {
		case domain.SyntaxGo:
			out, err = importsGo(root, src, file, g)
			return err
		case domain.SyntaxJava:
			out, err = importsJava(root, src, g)
			return err
		}
		out.Specs, err = extractImports(root, src, isTypeScriptPath(file), g)
		return err
	})
	if err != nil {
		return domain.FileImports{}, err
	}
	return out, nil
}

// withTree parses text with the grammar of file's extension and calls fn while the tree is
// alive; the nodes passed to fn must not be used after it returns. The parse and fn's passes
// (through the guard) stop when ctx ends, and the error then wraps ctx.Err().
func withTree(ctx context.Context, file, text string, fn func(root sitter.Node, src []byte, syntax domain.Syntax, g *guard) error) error {
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

	// Cancellation goes through the parser's cancellation flag, not ParseWithOptions: that call
	// saves its options in a global pointer table and never releases them, which leaks about
	// 186 bytes per parse in go-tree-sitter v0.25.0. The C parser keeps the flag pointer between
	// calls, so the word lives in C memory (cgo forbids C retaining a Go pointer) and is freed
	// only after the parser has dropped it and the AfterFunc can no longer write to it.
	flag := (*uintptr)(C.calloc(1, C.size_t(unsafe.Sizeof(uintptr(0)))))
	if flag == nil {
		return fmt.Errorf("parse: allocate cancellation flag")
	}
	parser.SetCancellationFlag(flag)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		atomic.StoreUintptr(flag, 1)
	})
	tree := parser.ParseWith(func(offset int, _ sitter.Point) []byte {
		if offset >= len(src) {
			return nil
		}
		return src[offset:min(offset+readChunk, len(src))]
	}, nil)
	if !stop() {
		<-done // the AfterFunc already started: wait until its store is over before freeing
	}
	parser.SetCancellationFlag(nil)
	C.free(unsafe.Pointer(flag))
	if tree == nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("parse: %w", err)
		}
		return fmt.Errorf("parse %s: no syntax tree", syntax)
	}
	defer tree.Close()
	return fn(*tree.RootNode(), src, syntax, &guard{ctx: ctx})
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

// children returns all children of n (named and anonymous) in order. It walks a cursor:
// ts_node_child(i) restarts at the first child, so indexing a wide node (an error-recovered file
// puts every token under the root) would be quadratic.
func children(n *sitter.Node) []*sitter.Node {
	cur := n.Walk()
	defer cur.Close()
	if !cur.GotoFirstChild() {
		return nil
	}
	var out []*sitter.Node
	for {
		out = append(out, cur.Node())
		if !cur.GotoNextSibling() {
			return out
		}
	}
}

// eachChild calls fn with every child of n in order until fn returns false. Unlike children it
// builds no slice, so a caller that checks the context in fn stops a wide node promptly.
func eachChild(n *sitter.Node, fn func(c *sitter.Node) bool) {
	cur := n.Walk()
	defer cur.Close()
	if !cur.GotoFirstChild() {
		return
	}
	for fn(cur.Node()) && cur.GotoNextSibling() {
	}
}

// firstChildKind returns the first child of n of one of the given kinds, or nil.
func firstChildKind(n *sitter.Node, kinds ...string) *sitter.Node {
	cur := n.Walk()
	defer cur.Close()
	if !cur.GotoFirstChild() {
		return nil
	}
	for {
		if c := cur.Node(); slices.Contains(kinds, c.Kind()) {
			return c
		}
		if !cur.GotoNextSibling() {
			return nil
		}
	}
}

// hasChildKind reports whether n has a child of the given kind.
func hasChildKind(n *sitter.Node, kind string) bool { return firstChildKind(n, kind) != nil }

// line returns the 1-based first line of n.
func line(n *sitter.Node) int { return int(n.StartPosition().Row) + 1 }

// endLine returns the 1-based last line of n.
func endLine(n *sitter.Node) int { return int(n.EndPosition().Row) + 1 }

// walk visits n and all its descendants in document order. visit returns false to skip the
// subtree of the node it was given. It returns the guard's error once the context is done.
func walk(root sitter.Node, g *guard, visit func(n *sitter.Node) bool) error {
	cur := root.Walk()
	defer cur.Close()
	for {
		if err := g.tick(); err != nil {
			return err
		}
		if visit(cur.Node()) && cur.GotoFirstChild() {
			continue
		}
		for !cur.GotoNextSibling() {
			if !cur.GotoParent() {
				return nil
			}
		}
	}
}
