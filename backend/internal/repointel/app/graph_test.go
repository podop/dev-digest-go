package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

func graphFixture() (*fakeClone, *fakeAnalyzer, []string) {
	clone := &fakeClone{files: map[string][]byte{
		"src/a.ts": []byte("import './b'"),
		"src/b.ts": []byte("export {}"),
		"src/c.ts": []byte("import './b'"),
	}, unreadable: map[string]bool{}}
	an := &fakeAnalyzer{imports: map[string][]string{
		"src/a.ts": {"./b", "react", "./missing"},
		"src/c.ts": {"./b"},
	}}
	return clone, an, []string{"src/a.ts", "src/b.ts", "src/c.ts"}
}

func TestImportGraph_BuildGraphResolvesWalkedFiles(t *testing.T) {
	clone, an, files := graphFixture()
	got, err := app.NewImportGraph(clone, an, nil).BuildGraph(t.Context(), clonePath, files)
	if err != nil {
		t.Fatalf("BuildGraph() error = %v", err)
	}
	want := domain.Graph{
		Edges:    []domain.Edge{{From: "src/a.ts", To: "src/b.ts"}, {From: "src/c.ts", To: "src/b.ts"}},
		Packages: []domain.FilePackage{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildGraph() mismatch (-want +got):\n%s", diff)
	}
}

// TestImportGraph_BuildGraphGoPackagesAndEdges: the package key the analyzer reports becomes a
// package row, and a Go import resolves through the clone's go.mod.
func TestImportGraph_BuildGraphGoPackagesAndEdges(t *testing.T) {
	clone := &fakeClone{files: map[string][]byte{
		"go.mod":        []byte("module example.com/r\n"),
		"cmd/main.go":   []byte("package main"),
		"internal/a.go": []byte("package internal"),
	}, unreadable: map[string]bool{}}
	an := &fakeAnalyzer{
		imports:  map[string][]string{"cmd/main.go": {"fmt", "example.com/r/internal"}},
		packages: map[string]string{"cmd/main.go": "go:cmd", "internal/a.go": "go:internal"},
	}
	got, err := app.NewImportGraph(clone, an, nil).BuildGraph(t.Context(), clonePath, []string{"cmd/main.go", "internal/a.go"})
	if err != nil {
		t.Fatalf("BuildGraph() error = %v", err)
	}
	want := domain.Graph{
		Edges:    []domain.Edge{{From: "cmd/main.go", To: "internal/a.go"}},
		Packages: []domain.FilePackage{{Path: "cmd/main.go", Key: "go:cmd"}, {Path: "internal/a.go", Key: "go:internal"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildGraph() mismatch (-want +got):\n%s", diff)
	}
}

func TestImportGraph_AFileThatFailsContributesNoSpecifiers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clone, an, files := graphFixture()
		clone.unreadable["src/a.ts"] = true
		an.hangImports = map[string]bool{"src/c.ts": true}
		got, err := app.NewImportGraph(clone, an, nil).BuildGraph(t.Context(), clonePath, files)
		if err != nil {
			t.Fatalf("BuildGraph() error = %v", err)
		}
		if len(got.Edges) != 0 || len(got.Packages) != 0 {
			t.Errorf("BuildGraph() = %v, want no edges and no packages (unreadable file, parse timeout)", got)
		}
	})
}

func TestImportGraph_MalformedTSConfigOnlyLogsAWarning(t *testing.T) {
	clone, an, files := graphFixture()
	clone.files["tsconfig.json"] = []byte("{ not json")
	var buf bytes.Buffer
	g := app.NewImportGraph(clone, an, slog.New(slog.NewTextHandler(&buf, nil)))
	got, err := g.BuildGraph(t.Context(), clonePath, files)
	if err != nil || len(got.Edges) != 2 {
		t.Fatalf("BuildGraph() = %v, %v, want 2 edges and no error", got.Edges, err)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "tsconfig") {
		t.Errorf("log = %q, want a warning about the tsconfig", buf.String())
	}
}

// TestImportGraph_AliasWarningCarriesNoTSConfigContent is NFR3005: text a clone's tsconfig holds (a
// key, a value, an extends string) reaches neither the returned error nor the log line, which has
// the problem categories and their number only.
func TestImportGraph_AliasWarningCarriesNoTSConfigContent(t *testing.T) {
	const marker = "TSCONFIG-CONTENT-MARKER"
	tests := []struct {
		name     string
		tsconfig string
		want     []string
	}{
		{"bad value", `{"compilerOptions": {"baseUrl": ` + marker + `}}`, []string{"reason=malformed", "disabled=true"}},
		{"bad key", `{"` + marker + `" 1}`, []string{"reason=malformed", "disabled=true"}},
		{"package extends", `{"extends": "@corp/` + marker + `"}`, []string{"reason=extends_not_relative", "disabled=false"}},
		{"extends outside", `{"extends": "../../` + marker + `"}`, []string{"reason=extends_outside", "problems=1"}},
		{"extends not found", `{"extends": ["./` + marker + `", "./` + marker + `-2"]}`, []string{"reason=extends_", "problems=2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clone, an, files := graphFixture()
			clone.files["tsconfig.json"] = []byte(tc.tsconfig)
			var buf bytes.Buffer
			g := app.NewImportGraph(clone, an, slog.New(slog.NewTextHandler(&buf, nil)))
			got, err := g.BuildGraph(t.Context(), clonePath, files)
			if err != nil || len(got.Edges) != 2 {
				t.Fatalf("BuildGraph() = %v, %v, want 2 edges and no error", got.Edges, err)
			}
			log := buf.String()
			if strings.Contains(log, marker) || strings.Contains(log, "error=") {
				t.Errorf("log = %q, want no tsconfig content", log)
			}
			if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "tsconfig aliases not used") {
				t.Errorf("log = %q, want the alias warning", log)
			}
			for _, w := range tc.want {
				if !strings.Contains(log, w) {
					t.Errorf("log = %q, want it to contain %q", log, w)
				}
			}
		})
	}
}

func TestImportGraph_PanicBecomesAnError(t *testing.T) {
	clone, an, files := graphFixture()
	an.panicImports = map[string]bool{"src/a.ts": true}
	_, err := app.NewImportGraph(clone, an, nil).BuildGraph(t.Context(), clonePath, files)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("BuildGraph() error = %v, want a panic error", err)
	}
}

func TestImportGraph_CancelledContextIsAnError(t *testing.T) {
	clone, an, files := graphFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := app.NewImportGraph(clone, an, nil).BuildGraph(ctx, clonePath, files)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BuildGraph() error = %v, want context.Canceled", err)
	}
}
