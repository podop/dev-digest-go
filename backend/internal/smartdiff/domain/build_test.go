package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/smartdiff/domain"
)

func TestBuild_GroupsFilesByRoleInFixedOrder(t *testing.T) {
	files := []domain.PullFile{
		{Path: "README.md", Additions: 1, Deletions: 0},
		{Path: "src/b.go", Additions: 10, Deletions: 2},
		{Path: "pnpm-lock.yaml", Additions: 100, Deletions: 50},
		{Path: "src/a.go", Additions: 3, Deletions: 4},
		{Path: "src/a_test.ts", Additions: 5, Deletions: 6},
		{Path: "src/foo.test.ts", Additions: 7, Deletions: 8},
		{Path: "vite.config.ts", Additions: 0, Deletions: 9},
	}
	findings := []domain.FindingRef{
		{File: "src/a.go", StartLine: 30},
		{File: "src/a.go", StartLine: 4},
		{File: "src/a.go", StartLine: 30}, // the same line twice is one line
		{File: "src/b.go", StartLine: 7},
		{File: "not/in/this/pr.go", StartLine: 1}, // ignored
	}
	want := domain.SmartDiff{
		TotalLines: 1 + 12 + 150 + 7 + 11 + 15 + 9,
		Groups: []domain.Group{
			{Role: domain.RoleCore, Files: []domain.FileEntry{
				// "_" sorts before "." in English collation: a_test.ts comes before a.go.
				{Path: "src/a_test.ts", Additions: 5, Deletions: 6, FindingLines: []int{}},
				{Path: "src/a.go", Additions: 3, Deletions: 4, FindingLines: []int{4, 30}},
				{Path: "src/b.go", Additions: 10, Deletions: 2, FindingLines: []int{7}},
			}},
			{Role: domain.RoleTests, Files: []domain.FileEntry{
				{Path: "src/foo.test.ts", Additions: 7, Deletions: 8, FindingLines: []int{}},
			}},
			{Role: domain.RoleWiring, Files: []domain.FileEntry{
				{Path: "vite.config.ts", Additions: 0, Deletions: 9, FindingLines: []int{}},
			}},
			{Role: domain.RoleDocs, Files: []domain.FileEntry{
				{Path: "README.md", Additions: 1, Deletions: 0, FindingLines: []int{}},
			}},
			{Role: domain.RoleBoilerplate, Files: []domain.FileEntry{
				{Path: "pnpm-lock.yaml", Additions: 100, Deletions: 50, FindingLines: []int{}},
			}},
		},
	}

	got := domain.Build(files, findings)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Build mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_NoFilesGivesFiveEmptyGroups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []domain.PullFile
	}{{"nil", nil}, {"empty", []domain.PullFile{}}} {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Build(tc.files, []domain.FindingRef{{File: "a.go", StartLine: 1}})
			if got.TotalLines != 0 || len(got.Groups) != 5 {
				t.Fatalf("Build(no files) = %d groups, %d lines, want 5 groups, 0 lines", len(got.Groups), got.TotalLines)
			}
			for i, role := range domain.Roles() {
				g := got.Groups[i]
				if g.Role != role || g.Files == nil || len(g.Files) != 0 {
					t.Errorf("group %d = {%q, %#v}, want role %q and an empty non-nil file list", i, g.Role, g.Files, role)
				}
			}
		})
	}
}

// Files sort by English collation (localeCompare), not by bytes: case is a tie-breaker after the
// letters, and punctuation sorts before digits and letters.
func TestBuild_SortsFilesByEnglishCollation(t *testing.T) {
	paths := []string{"src/b.go", "src/B.go", "src/a.go", "src/A.go", "src/_x.go", "src/10.go", "src/9.go", "src/é.go", "src/f.go"}
	files := make([]domain.PullFile, len(paths))
	for i, p := range paths {
		files[i] = domain.PullFile{Path: p}
	}
	got := domain.Build(files, nil)
	var order []string
	for _, f := range got.Groups[0].Files {
		order = append(order, f.Path)
	}
	// The expected order is what Node's localeCompare gives for these paths.
	want := []string{"src/_x.go", "src/10.go", "src/9.go", "src/a.go", "src/A.go", "src/b.go", "src/B.go", "src/é.go", "src/f.go"}
	if diff := cmp.Diff(want, order); diff != "" {
		t.Errorf("Build file order mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_DoesNotModifyItsInput(t *testing.T) {
	files := []domain.PullFile{{Path: "b.go"}, {Path: "a.go"}}
	domain.Build(files, nil)
	if files[0].Path != "b.go" || files[1].Path != "a.go" {
		t.Errorf("Build reordered its input: %v", files)
	}
}
