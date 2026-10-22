package parity_test

// S23 (AC4041, NFR4005 "CallerSignatures reference rules"): the reference finder and the caller
// scan against the Node goldens (callers.json). The scan runs the real clone walk and the real
// tree-sitter extractor over a copy of the corpus fixture in a temp dir.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestCallersParity_ReferenceLines(t *testing.T) {
	var g struct {
		References []struct {
			Name    string `json:"name"`
			Symbol  string `json:"symbol"`
			Content string `json:"content"`
			Lines   []int  `json:"lines"`
		} `json:"references"`
	}
	loadGolden(t, "callers", &g)
	if len(g.References) == 0 {
		t.Fatal("callers.json has no references cases")
	}
	for _, tc := range g.References {
		t.Run(tc.Name, func(t *testing.T) {
			got := domain.ReferenceLines(tc.Content, tc.Symbol)
			want := tc.Lines
			if tc.Symbol == "" {
				// Node's pattern with an empty name matches any "(" (line 2 here). The scan never asks
				// for an empty name (symbols are non-empty), and Go deliberately answers no references
				// (domain/callers_test.go "empty symbol"), so this edge case is the one documented
				// divergence of the reference rules.
				if !cmp.Equal(want, []int{2}) {
					t.Fatalf("golden for the empty symbol is %v, the divergence below was written for [2]", want)
				}
				want = nil
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ReferenceLines(%q, %q) mismatch (-want +got):\n%s", tc.Content, tc.Symbol, diff)
			}
		})
	}
}

// callerStore serves the one repo of a case and its file ranks; the rest of the read port is
// not used by CallerSignatures.
type callerStore struct {
	repo  domain.Repo
	ranks []domain.PathPercentile
}

func (s *callerStore) GetRepo(_ context.Context, id string) (domain.Repo, error) {
	if id != s.repo.ID {
		return domain.Repo{}, domain.ErrRepoNotFound
	}
	return s.repo, nil
}

func (s *callerStore) GetState(context.Context, string) (domain.IndexState, bool, error) {
	return domain.IndexState{}, false, nil
}

func (s *callerStore) CachedMap(context.Context, string, string, int) (domain.RepoMap, bool, error) {
	return domain.RepoMap{}, false, nil
}

func (s *callerStore) FileRanks(_ context.Context, _ string, paths []string) ([]domain.PathPercentile, error) {
	var out []domain.PathPercentile
	for _, r := range s.ranks {
		for _, p := range paths {
			if r.Path == p {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (s *callerStore) SymbolsInFiles(context.Context, string, []string) ([]domain.SymbolInfo, error) {
	return nil, nil
}

func (s *callerStore) RankedPaths(context.Context, string, int) ([]domain.RankedPath, error) {
	return nil, nil
}

func (s *callerStore) Edges(context.Context, string) ([]domain.Edge, error) { return nil, nil }

func (s *callerStore) ResolvedCallers(context.Context, string, []string, []string) ([]domain.ResolvedCaller, error) {
	return nil, nil
}

func (s *callerStore) FileFacts(context.Context, string, []string) ([]domain.FileFacts, error) {
	return nil, nil
}

// copyTree copies the fixture directory src into dst.
func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", src, err)
	}
}

func TestCallersParity_CallerSignatures(t *testing.T) {
	var g struct {
		Signatures []struct {
			Name         string   `json:"name"`
			Fixture      string   `json:"fixture"`
			ChangedFiles []string `json:"changedFiles"`
			Limit        *int     `json:"limit"`
			Enabled      *bool    `json:"enabled"`
			NoClone      bool     `json:"noClone"`
			Ranks        []struct {
				Path       string `json:"path"`
				Percentile int    `json:"percentile"`
			} `json:"ranks"`
			ExtraFiles []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
				PadTo   int    `json:"padTo"`
				Link    string `json:"link"`
			} `json:"extraFiles"`
			Rows []struct {
				File      string `json:"file"`
				Symbol    string `json:"symbol"`
				Signature string `json:"signature"`
				Rank      int    `json:"rank"`
			} `json:"rows"`
		} `json:"signatures"`
	}
	loadGolden(t, "callers", &g)
	if len(g.Signatures) == 0 {
		t.Fatal("callers.json has no signatures cases")
	}
	for _, tc := range g.Signatures {
		t.Run(tc.Name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repo")
			copyTree(t, filepath.Join("testdata", "corpus", "callers", "fixtures", tc.Fixture), root)
			for _, f := range tc.ExtraFiles {
				full := filepath.Join(root, filepath.FromSlash(f.Path))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				switch {
				case f.Link != "":
					if err := os.Symlink(f.Link, full); err != nil {
						t.Fatalf("symlink %s: %v", f.Path, err)
					}
				case f.PadTo > 0:
					// content followed by a "//" comment filling the file up to PadTo bytes
					pad := f.PadTo - len(f.Content) - 2
					if err := os.WriteFile(full, []byte(f.Content+"//"+strings.Repeat("p", pad)), 0o644); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}

			store := &callerStore{repo: domain.Repo{ID: "repo-1", Owner: "acme", Name: "shop", ClonePath: root}}
			if tc.NoClone {
				store.repo.ClonePath = ""
			}
			for _, r := range tc.Ranks {
				store.ranks = append(store.ranks, domain.PathPercentile{Path: r.Path, Percentile: r.Percentile})
			}
			enabled := tc.Enabled == nil || *tc.Enabled
			reader := app.NewReader(store, enabled, app.WithCallerScan(app.CallerScan{
				Repos: store, Files: clonetree.New(), Analyzer: treesitter.New(),
			}))
			limit := 0
			if tc.Limit != nil {
				limit = *tc.Limit
			}
			got, err := reader.CallerSignatures(t.Context(), "repo-1", tc.ChangedFiles, limit)
			if err != nil {
				t.Fatalf("CallerSignatures(%v, %d) error = %v", tc.ChangedFiles, limit, err)
			}
			var want []domain.CallerSignature
			for _, r := range tc.Rows {
				want = append(want, domain.CallerSignature{File: r.File, Symbol: r.Symbol, Signature: r.Signature, Rank: r.Rank})
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("CallerSignatures(%v, %d) mismatch (-want +got):\n%s", tc.ChangedFiles, limit, diff)
			}
		})
	}
}
