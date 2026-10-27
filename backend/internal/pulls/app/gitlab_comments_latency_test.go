//go:build !race

package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// AC14, NFR8: DevDigest's own processing of a GitLab post (body rules, patch mapping) stays within
// 50 ms at p95 for a ~1 MiB body and a 3,000-line patch. The fake GitLab answers instantly, so the
// time measured is DevDigest's. The race detector slows the code several times over, hence the
// build tag (A9); the run is skipped under -short.
func TestPostComment_gitlabProcessingLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test: skipped under -short")
	}
	const runs = 50
	const p95Limit = 50 * time.Millisecond

	var patch strings.Builder
	patch.WriteString("@@ -1,1500 +1,2000 @@\n")
	for i := range 1500 {
		fmt.Fprintf(&patch, " context %d\n", i)
	}
	for i := range 500 {
		fmt.Fprintf(&patch, "+added %d\n", i)
	}
	if n := strings.Count(patch.String(), "\n"); n < 2000 {
		t.Fatalf("patch has %d lines, want a large one", n)
	}
	big := patch.String() + strings.Repeat(" more context\n", 1000) // 3,000+ lines

	bodies := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"accepted body", strings.Repeat("see @ann and <b>x</b> /not-start ![i](u)\n/approve\n", 12_000), false},
		{"1 MiB body over the limit", strings.Repeat("see @ann and <b>x</b> /x ![i](u)\n/approve\n", 1<<20/42+1), true},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			f.st.files[f.pr.ID] = []domain.File{{Path: "src/a.go", Patch: &big}}
			// The last added line sits at new number 2000 in the first hunk.
			req := app.CommentRequest{Path: "src/a.go", Line: 1999, Body: tc.body}

			durations := make([]time.Duration, 0, runs)
			for range runs {
				start := time.Now()
				_, err := f.post(t, req)
				durations = append(durations, time.Since(start))
				if (err != nil) != tc.wantErr {
					t.Fatalf("PostComment() error = %v, want error %t", err, tc.wantErr)
				}
			}
			slices.Sort(durations)
			if p95 := durations[runs*95/100-1]; p95 > p95Limit {
				t.Errorf("p95 of %d posts = %v, want at most %v", runs, p95, p95Limit)
			}
		})
	}
}
