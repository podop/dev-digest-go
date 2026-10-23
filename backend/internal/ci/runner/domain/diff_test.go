package domain_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// fixtureDiff is FIXTURE_DIFF_RAW of the Node runner's diff.test.ts.
const fixtureDiff = `diff --git a/src/config.ts b/src/config.ts
--- a/src/config.ts
+++ b/src/config.ts
@@ -9,3 +9,4 @@
 host: 'localhost',
+apiKey: 'sk_live_abcdef123456',
 port: 3000,
 timeout: 30,
`

func TestParseDiff(t *testing.T) {
	t.Run("single file, single hunk", func(t *testing.T) {
		got := domain.ParseDiff(fixtureDiff)
		want := reviewer.Diff{Raw: fixtureDiff, Files: []reviewer.DiffFile{{
			Path: "src/config.ts", Additions: 1,
			Hunks: []reviewer.Hunk{{
				File: "src/config.ts", OldStart: 9, OldLines: 3, NewStart: 9, NewLines: 4,
				NewLineNumbers: []int{9, 10, 11, 12}, // context, added, context, context
			}},
		}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ParseDiff(fixture) mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("the final line break is not a context line", func(t *testing.T) {
		// Phase 4 rules would give 13 as well; the runner must stop at the hunk's last line.
		for _, l := range domain.ParseDiff(fixtureDiff).Files[0].Hunks[0].NewLineNumbers {
			if l == 13 {
				t.Errorf("ParseDiff(fixture) covers line 13, past the end of the hunk")
			}
		}
		phase4 := reviewer.ParseUnifiedDiff(fixtureDiff).Files[0].Hunks[0].NewLineNumbers
		if len(phase4) != 5 {
			t.Fatalf("premise: Phase 4 ParseUnifiedDiff covers %v, want the phantom line 13", phase4)
		}
	})

	t.Run("only one final line break is dropped", func(t *testing.T) {
		raw := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,1 +1,2 @@\n x\n+y\n\n"
		got := domain.ParseDiff(raw).Files[0].Hunks[0].NewLineNumbers
		if want := []int{1, 2, 3}; !cmp.Equal(want, got) {
			t.Errorf("ParseDiff(blank line before the final break) lines = %v, want %v", got, want)
		}
	})

	t.Run("without a final line break", func(t *testing.T) {
		raw := strings.TrimSuffix(fixtureDiff, "\n")
		if got := domain.ParseDiff(raw).Files[0].Hunks[0].NewLineNumbers; !cmp.Equal([]int{9, 10, 11, 12}, got) {
			t.Errorf("ParseDiff(no final break) lines = %v, want [9 10 11 12]", got)
		}
	})

	t.Run("multiple files", func(t *testing.T) {
		raw := "diff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ -1,1 +1,2 @@\n line one\n+line two\n" +
			"diff --git a/b.ts b/b.ts\n--- a/b.ts\n+++ b/b.ts\n@@ -5,2 +5,2 @@\n-old line\n+new line\n unchanged\n"
		got := domain.ParseDiff(raw)
		var paths []string
		for _, f := range got.Files {
			paths = append(paths, f.Path)
		}
		if want := []string{"a.ts", "b.ts"}; !cmp.Equal(want, paths) {
			t.Errorf("ParseDiff() paths = %v, want %v", paths, want)
		}
		if b := got.Files[1]; b.Deletions != 1 || b.Additions != 1 {
			t.Errorf("ParseDiff() b.ts = +%d -%d, want +1 -1", b.Additions, b.Deletions)
		}
	})

	t.Run("empty and header-less input", func(t *testing.T) {
		for _, raw := range []string{"", "\n", "just text\n"} {
			if got := domain.ParseDiff(raw); len(got.Files) != 0 || got.Raw != raw {
				t.Errorf("ParseDiff(%q) = %+v, want no files and the same Raw", raw, got)
			}
		}
	})
}

const stripInput = `diff --git a/src/config.ts b/src/config.ts
--- a/src/config.ts
+++ b/src/config.ts
@@ -1,1 +1,2 @@
 keep me
+real change
diff --git a/.devdigest/runner/index.js b/.devdigest/runner/index.js
--- a/.devdigest/runner/index.js
+++ b/.devdigest/runner/index.js
@@ -1,1 +1,1 @@
-old bundle
+new bundle
diff --git a/.github/workflows/devdigest-review.yml b/.github/workflows/devdigest-review.yml
--- a/.github/workflows/devdigest-review.yml
+++ b/.github/workflows/devdigest-review.yml
@@ -1,1 +1,2 @@
 name: DevDigest Review
+on: pull_request
`

func TestStripIgnoredFiles(t *testing.T) {
	t.Run("drops the runner directory and the generated workflow, and with the last section its final line break", func(t *testing.T) {
		got := domain.StripIgnoredFiles(stripInput)
		want := "diff --git a/src/config.ts b/src/config.ts\n--- a/src/config.ts\n+++ b/src/config.ts\n@@ -1,1 +1,2 @@\n keep me\n+real change"
		if got != want {
			t.Errorf("StripIgnoredFiles() = %q, want %q", got, want)
		}
		parsed := domain.ParseDiff(got)
		if len(parsed.Files) != 1 || parsed.Files[0].Path != "src/config.ts" || parsed.Files[0].Additions != 1 {
			t.Errorf("ParseDiff(StripIgnoredFiles()) files = %+v, want only src/config.ts with one addition", parsed.Files)
		}
	})

	t.Run("no-op when nothing is ignored", func(t *testing.T) {
		if got := domain.StripIgnoredFiles(fixtureDiff); got != fixtureDiff {
			t.Errorf("StripIgnoredFiles(fixture) changed the text: %q", got)
		}
	})

	t.Run("a PR touching only DevDigest files reviews an empty diff", func(t *testing.T) {
		only := stripInput[strings.Index(stripInput, "diff --git a/.devdigest"):]
		if got := domain.ParseDiff(domain.StripIgnoredFiles(only)); len(got.Files) != 0 {
			t.Errorf("files = %+v, want none", got.Files)
		}
	})

	tests := []struct {
		name   string
		header string
		strip  bool
	}{
		{"runner dir", "diff --git a/.devdigest/x b/.devdigest/x", true},
		{"memory file", "diff --git a/.devdigest/memory.jsonl b/.devdigest/memory.jsonl", true},
		{"workflow dir", "diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml", true},
		{"rename into the ignored dir", "diff --git a/old.yml b/.github/workflows/new.yml", true},
		{"rename out of the ignored dir", "diff --git a/.devdigest/old b/src/new", false},
		{"other .github files", "diff --git a/.github/CODEOWNERS b/.github/CODEOWNERS", false},
		{"similar prefix", "diff --git a/.devdigestx/a b/.devdigestx/a", false},
		{"nested", "diff --git a/src/.devdigest/a b/src/.devdigest/a", false},
		{"surrounding white space of the path is trimmed", "diff --git a/x b/ .devdigest/a ", true},
		{"no new-side path", "diff --git nothing", false},
		{"the first b/ wins", "diff --git a/x b/.devdigest/a b/c", true},
		{"CR after the path hides the header match", "diff --git a/.devdigest/a b/.devdigest/a\r", false},
		{"a later b/ after a CR is tried", "diff --git a/x b/y\r b/.devdigest/a", true},
		{"U+2028 in the path hides the match", "diff --git a/.devdigest/a b/.devdigest/a ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.header + "\n+body\n"
			want := raw
			if tc.strip {
				want = ""
			}
			if got := domain.StripIgnoredFiles(raw); got != want {
				t.Errorf("StripIgnoredFiles(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}
