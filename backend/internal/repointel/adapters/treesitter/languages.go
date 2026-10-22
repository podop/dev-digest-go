// Package treesitter extracts symbols, references and import specifiers from TypeScript and
// JavaScript sources with the tree-sitter grammars through the CGO go-tree-sitter binding. The
// binding has no finalizers: every Parser, Tree and TreeCursor created here is closed.
package treesitter

import (
	"fmt"

	sitter "github.com/tree-sitter/go-tree-sitter"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Language returns the tree-sitter language of a syntax. A Language is immutable and may be
// shared by parsers on different goroutines; a Parser may not.
func Language(s domain.Syntax) (*sitter.Language, error) {
	switch s {
	case domain.SyntaxTypeScript:
		return sitter.NewLanguage(typescript.LanguageTypescript()), nil
	case domain.SyntaxTSX:
		return sitter.NewLanguage(typescript.LanguageTSX()), nil
	case domain.SyntaxJavaScript:
		return sitter.NewLanguage(javascript.Language()), nil
	}
	return nil, fmt.Errorf("no tree-sitter grammar for syntax %d", int(s))
}
