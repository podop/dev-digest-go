package treesitter

import (
	"context"
	"errors"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/domain"
)

func TestGuard_ChecksEveryCheckEveryNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	g := &guard{ctx: ctx}
	for i := 1; i < checkEvery; i++ {
		if err := g.tick(); err != nil {
			t.Fatalf("tick #%d = %v, want nil before the %dth", i, err, checkEvery)
		}
	}
	if err := g.tick(); !errors.Is(err, context.Canceled) {
		t.Errorf("tick #%d = %v, want context.Canceled", checkEvery, err)
	}
}

// TestPasses_StopOnADoneContext runs each pass of the extraction on a finished tree with a
// guard whose context is already done; a file with enough nodes must make every pass return the
// context's error instead of a result.
func TestPasses_StopOnADoneContext(t *testing.T) {
	src := strings.Repeat("import {a} from 'x';\nexport function f(a) { return g(a); }\nconst h = () => a();\nexport { f };\n", 200)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	done := func() *guard { return &guard{ctx: cancelled} }

	err := withTree(t.Context(), "f.ts", src, func(root sitter.Node, b []byte, syntax domain.Syntax, _ *guard) error {
		if _, err := extractSymbols(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractSymbols error = %v, want context.Canceled", err)
		}
		if _, err := extractReferences(root, b, syntax, nil, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractReferences error = %v, want context.Canceled", err)
		}
		if _, err := extractImports(root, b, true, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractImports(ts) error = %v, want context.Canceled", err)
		}
		if _, err := extractImports(root, b, false, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractImports(js) error = %v, want context.Canceled", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withTree error = %v", err)
	}
}

// TestGoPasses_StopOnADoneContext: the same for the Go passes (FR4).
func TestGoPasses_StopOnADoneContext(t *testing.T) {
	src := "package p\n" + strings.Repeat("import \"x\"\nfunc f(a int) { g(a, h.i); _ = T{} }\ntype S struct{}\n", 400)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	done := func() *guard { return &guard{ctx: cancelled} }

	err := withTree(t.Context(), "p/f.go", src, func(root sitter.Node, b []byte, _ domain.Syntax, _ *guard) error {
		if _, err := goSymbols(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("goSymbols error = %v, want context.Canceled", err)
		}
		if _, err := goReferences(root, b, nil, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("goReferences error = %v, want context.Canceled", err)
		}
		if _, err := extractGo(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractGo error = %v, want context.Canceled", err)
		}
		if _, err := importsGo(root, b, "p/f.go", done()); !errors.Is(err, context.Canceled) {
			t.Errorf("importsGo error = %v, want context.Canceled", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withTree error = %v", err)
	}
}

// TestJavaPasses_StopOnADoneContext: the same for the Java passes (FR4).
func TestJavaPasses_StopOnADoneContext(t *testing.T) {
	src := "package p;\n" + strings.Repeat("import a.b.C;\n", 1200) +
		strings.Repeat("@A interface I { void f(); }\nclass S { S() { g(new T()); } }\n", 400)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	done := func() *guard { return &guard{ctx: cancelled} }

	err := withTree(t.Context(), "p/F.java", src, func(root sitter.Node, b []byte, _ domain.Syntax, _ *guard) error {
		if _, err := javaSymbols(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("javaSymbols error = %v, want context.Canceled", err)
		}
		if _, err := javaReferences(root, b, nil, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("javaReferences error = %v, want context.Canceled", err)
		}
		if _, err := extractJava(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("extractJava error = %v, want context.Canceled", err)
		}
		if _, err := importsJava(root, b, done()); !errors.Is(err, context.Canceled) {
			t.Errorf("importsJava error = %v, want context.Canceled", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withTree error = %v", err)
	}
}
