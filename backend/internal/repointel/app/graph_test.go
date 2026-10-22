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

func TestImportGraph_BuildEdgesResolvesWalkedFiles(t *testing.T) {
	clone, an, files := graphFixture()
	got, err := app.NewImportGraph(clone, an, nil).BuildEdges(t.Context(), clonePath, files)
	if err != nil {
		t.Fatalf("BuildEdges() error = %v", err)
	}
	want := []domain.Edge{{From: "src/a.ts", To: "src/b.ts"}, {From: "src/c.ts", To: "src/b.ts"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildEdges() mismatch (-want +got):\n%s", diff)
	}
}

func TestImportGraph_AFileThatFailsContributesNoSpecifiers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clone, an, files := graphFixture()
		clone.unreadable["src/a.ts"] = true
		an.hangImports = map[string]bool{"src/c.ts": true}
		got, err := app.NewImportGraph(clone, an, nil).BuildEdges(t.Context(), clonePath, files)
		if err != nil {
			t.Fatalf("BuildEdges() error = %v", err)
		}
		if len(got) != 0 {
			t.Errorf("BuildEdges() = %v, want no edges (unreadable file, parse timeout)", got)
		}
	})
}

func TestImportGraph_MalformedTSConfigOnlyLogsAWarning(t *testing.T) {
	clone, an, files := graphFixture()
	clone.files["tsconfig.json"] = []byte("{ not json")
	var buf bytes.Buffer
	g := app.NewImportGraph(clone, an, slog.New(slog.NewTextHandler(&buf, nil)))
	got, err := g.BuildEdges(t.Context(), clonePath, files)
	if err != nil || len(got) != 2 {
		t.Fatalf("BuildEdges() = %v, %v, want 2 edges and no error", got, err)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "tsconfig") {
		t.Errorf("log = %q, want a warning about the tsconfig", buf.String())
	}
}

func TestImportGraph_PanicBecomesAnError(t *testing.T) {
	clone, an, files := graphFixture()
	an.panicImports = map[string]bool{"src/a.ts": true}
	_, err := app.NewImportGraph(clone, an, nil).BuildEdges(t.Context(), clonePath, files)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("BuildEdges() error = %v, want a panic error", err)
	}
}

func TestImportGraph_CancelledContextIsAnError(t *testing.T) {
	clone, an, files := graphFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := app.NewImportGraph(clone, an, nil).BuildEdges(ctx, clonePath, files)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BuildEdges() error = %v, want context.Canceled", err)
	}
}
