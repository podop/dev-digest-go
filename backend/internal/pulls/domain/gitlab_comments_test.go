package domain_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/domain"
)

func ip(n int) *int { return &n }

func i64p(n int64) *int64 { return &n }

func sp(s string) *string { return &s }

const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func diffNote(id int64, user string, pos *domain.Position) domain.Note {
	return domain.Note{ID: id, Type: "DiffNote", Body: fmt.Sprintf("body %d", id), Author: user,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Position: pos}
}

func textPos(headSHA, newPath, oldPath string, oldLine, newLine *int) *domain.Position {
	return &domain.Position{Type: "text", HeadSHA: headSHA, NewPath: newPath, OldPath: oldPath, OldLine: oldLine, NewLine: newLine}
}

// EC1, AC1: only the text diff discussion survives; replies carry the root's placement.
func TestMapDiscussions_KeepsTextDiffThreads(t *testing.T) {
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ds := []domain.Discussion{
		{ID: "d1", Notes: []domain.Note{{ID: 1, System: true, Body: "added 1 commit"}}},
		{ID: "d2", Notes: []domain.Note{{ID: 2, Type: "", Body: "general"}}},
		{ID: "d3", Notes: []domain.Note{{ID: 3, Type: "DiffNote", Position: &domain.Position{Type: "image", HeadSHA: head, NewPath: "a.png"}}}},
		{ID: "d4", Notes: []domain.Note{{ID: 4, Type: "DiffNote", Position: &domain.Position{Type: "file", HeadSHA: head, NewPath: "a.bin"}}}},
		{ID: "d5", Notes: []domain.Note{
			diffNote(10, "ann", textPos(head, "src/a.ts", "src/a.ts", nil, ip(5))),
			{ID: 11, Type: "DiscussionNote", Body: "reply one", Author: "bob", CreatedAt: created},
			{ID: 12, System: true, Body: "changed this line"},
			{ID: 13, Type: "DiscussionNote", Body: "reply two", Author: "", CreatedAt: created},
		}},
	}
	got := domain.MapDiscussions(ds, head, "https://gitlab.example/g/p/", 7)
	root := domain.ReviewComment{ID: 10, Path: "src/a.ts", Line: ip(5), OriginalLine: ip(5), Side: "RIGHT", Body: "body 10",
		User: "ann", CreatedAt: created, HTMLURL: "https://gitlab.example/g/p/-/merge_requests/7#note_10"}
	want := []domain.ReviewComment{
		root,
		{ID: 11, Path: "src/a.ts", Line: ip(5), OriginalLine: ip(5), Side: "RIGHT", Body: "reply one", User: "bob", CreatedAt: created,
			HTMLURL: "https://gitlab.example/g/p/-/merge_requests/7#note_11", InReplyToID: i64p(10)},
		{ID: 13, Path: "src/a.ts", Line: ip(5), OriginalLine: ip(5), Side: "RIGHT", Body: "reply two", User: "unknown", CreatedAt: created,
			HTMLURL: "https://gitlab.example/g/p/-/merge_requests/7#note_13", InReplyToID: i64p(10)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MapDiscussions mismatch (-want +got):\n%s", diff)
	}
}

// AC3, EC3: head comparison, LEFT-only roots, path fallback.
func TestMapDiscussions_PlacementAndOutdated(t *testing.T) {
	const older = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name      string
		pos       *domain.Position
		diffHead  string
		wantPath  string
		wantSide  string
		wantLine  *int
		wantOrig  *int
		wantStale bool
	}{
		{"current head, added line", textPos(head, "n.go", "o.go", nil, ip(3)), head, "n.go", "RIGHT", ip(3), ip(3), false},
		{"unchanged line uses the new number", textPos(head, "n.go", "o.go", ip(2), ip(3)), head, "n.go", "RIGHT", ip(3), ip(3), false},
		{"removed line only is LEFT", textPos(head, "", "o.go", ip(7), nil), head, "o.go", "LEFT", ip(7), ip(7), false},
		{"older head is outdated and has no line", textPos(older, "n.go", "n.go", nil, ip(3)), head, "n.go", "RIGHT", nil, ip(3), true},
		{"outdated LEFT keeps original line", textPos(older, "", "o.go", ip(7), nil), head, "o.go", "LEFT", nil, ip(7), true},
		{"fallback head compared", textPos(head, "n.go", "n.go", nil, ip(1)), "cccccccccccccccccccccccccccccccccccccccc", "n.go", "RIGHT", nil, ip(1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := []domain.Discussion{{ID: "d", Notes: []domain.Note{diffNote(1, "ann", tc.pos), diffNote(2, "bob", nil)}}}
			ds[0].Notes[1].Type = "DiscussionNote"
			got := domain.MapDiscussions(ds, tc.diffHead, "https://h/p", 1)
			if len(got) != 2 {
				t.Fatalf("MapDiscussions = %d comments, want 2", len(got))
			}
			for _, c := range got {
				if c.Path != tc.wantPath || c.Side != tc.wantSide || c.IsOutdated != tc.wantStale {
					t.Errorf("comment %d = path %q side %q outdated %v, want %q %q %v", c.ID, c.Path, c.Side, c.IsOutdated, tc.wantPath, tc.wantSide, tc.wantStale)
				}
				if diff := cmp.Diff(tc.wantLine, c.Line); diff != "" {
					t.Errorf("comment %d line mismatch (-want +got):\n%s", c.ID, diff)
				}
				if diff := cmp.Diff(tc.wantOrig, c.OriginalLine); diff != "" {
					t.Errorf("comment %d original_line mismatch (-want +got):\n%s", c.ID, diff)
				}
			}
		})
	}
}

func TestMapDiscussions_DropsMalformedRoots(t *testing.T) {
	ds := []domain.Discussion{
		{ID: "empty"},
		{ID: "nopos", Notes: []domain.Note{{ID: 1, Type: "DiffNote"}}},
		{ID: "nopath", Notes: []domain.Note{diffNote(2, "a", &domain.Position{Type: "text", HeadSHA: head})}},
		{ID: "systemroot", Notes: []domain.Note{{ID: 3, Type: "DiffNote", System: true, Position: textPos(head, "a", "a", nil, ip(1))}}},
	}
	if got := domain.MapDiscussions(ds, head, "https://h/p", 1); len(got) != 0 {
		t.Errorf("MapDiscussions(malformed) = %d comments, want 0: %+v", len(got), got)
	}
}

func TestMapDiscussions_NoWebURLNoLink(t *testing.T) {
	ds := []domain.Discussion{{ID: "d", Notes: []domain.Note{diffNote(1, "a", textPos(head, "a", "a", nil, ip(1)))}}}
	got := domain.MapDiscussions(ds, head, "", 1)
	if len(got) != 1 || got[0].HTMLURL != "" {
		t.Errorf("MapDiscussions(no web url) = %+v, want one comment with empty html_url", got)
	}
}

// EC2, AC2: the first 100 comments in GitLab order, across discussions and inside one.
func TestMapDiscussions_CapsAtHundred(t *testing.T) {
	var ds []domain.Discussion
	for i := range 150 {
		ds = append(ds, domain.Discussion{ID: fmt.Sprint(i), Notes: []domain.Note{diffNote(int64(i+1), "a", textPos(head, "a", "a", nil, ip(1)))}})
	}
	got := domain.MapDiscussions(ds, head, "https://h/p", 1)
	if len(got) != 100 || got[0].ID != 1 || got[99].ID != 100 {
		t.Errorf("MapDiscussions(150 threads) = %d comments (first %d), want the first 100", len(got), got[0].ID)
	}

	thread := domain.Discussion{ID: "t", Notes: []domain.Note{diffNote(1, "a", textPos(head, "a", "a", nil, ip(1)))}}
	for i := 2; i <= 120; i++ {
		thread.Notes = append(thread.Notes, domain.Note{ID: int64(i), Type: "DiscussionNote", Body: "r"})
	}
	got = domain.MapDiscussions([]domain.Discussion{thread}, head, "https://h/p", 1)
	if len(got) != 100 || got[99].ID != 100 {
		t.Errorf("MapDiscussions(one thread of 120) = %d comments, want 100", len(got))
	}
}

func TestMapDiscussions_DoesNotShareLinePointers(t *testing.T) {
	pos := textPos(head, "a", "a", nil, ip(4))
	ds := []domain.Discussion{{ID: "d", Notes: []domain.Note{diffNote(1, "a", pos), {ID: 2, Type: "DiscussionNote"}}}}
	got := domain.MapDiscussions(ds, head, "https://h/p", 1)
	*got[0].Line = 99
	if *got[1].Line != 4 || *pos.NewLine != 4 {
		t.Errorf("mutating one comment's line changed another or the input: %d, %d", *got[1].Line, *pos.NewLine)
	}
}

func TestDiffRefsComplete(t *testing.T) {
	tests := []struct {
		name string
		refs domain.DiffRefs
		want bool
	}{
		{"all", domain.DiffRefs{BaseSHA: "a", StartSHA: "b", HeadSHA: "c"}, true},
		{"empty", domain.DiffRefs{}, false},
		{"no base", domain.DiffRefs{StartSHA: "b", HeadSHA: "c"}, false},
		{"no start", domain.DiffRefs{BaseSHA: "a", HeadSHA: "c"}, false},
		{"no head", domain.DiffRefs{BaseSHA: "a", StartSHA: "b"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.refs.Complete(); got != tc.want {
				t.Errorf("%+v.Complete() = %v, want %v", tc.refs, got, tc.want)
			}
		})
	}
}

func TestPullEffectiveDiffHead(t *testing.T) {
	tests := []struct {
		name string
		p    domain.Pull
		want string
	}{
		{"stored diff head wins", domain.Pull{HeadSHA: "h", DiffHead: sp("d")}, "d"},
		{"no diff head: head sha", domain.Pull{HeadSHA: "h"}, "h"},
		{"empty diff head: head sha", domain.Pull{HeadSHA: "h", DiffHead: sp("")}, "h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.EffectiveDiffHead(); got != tc.want {
				t.Errorf("EffectiveDiffHead() = %q, want %q", got, tc.want)
			}
		})
	}
}

// EC8: a renamed file keeps both paths; others use the path twice.
func TestFilePositionOldPath(t *testing.T) {
	if got := (domain.File{Path: "new/a.ts", PreviousPath: sp("old/a.ts")}).PositionOldPath(); got != "old/a.ts" {
		t.Errorf("renamed PositionOldPath() = %q, want old/a.ts", got)
	}
	for _, f := range []domain.File{{Path: "a.ts"}, {Path: "a.ts", PreviousPath: sp("")}} {
		if got := f.PositionOldPath(); got != "a.ts" {
			t.Errorf("%+v PositionOldPath() = %q, want a.ts", f, got)
		}
	}
}

const samplePatch = `@@ -1,4 +1,5 @@
 one
-two
+TWO
+two and a half
 three
 four
@@ -10,3 +11,3 @@ func x() {
 ten
-eleven
+ELEVEN
 twelve
\ No newline at end of file
`

func TestPositionFor(t *testing.T) {
	f := domain.File{Path: "a.go", Patch: sp(samplePatch)}
	tests := []struct {
		name     string
		side     string
		line     int
		wantOld  *int
		wantNew  *int
		wantWhy  string
		patchNil bool
	}{
		{"added line, default side", "", 3, nil, ip(3), "", false},
		{"second added line RIGHT", "RIGHT", 2, nil, ip(2), "", false},
		{"unchanged RIGHT carries old number", "RIGHT", 5, ip(4), ip(5), "", false},
		{"unchanged RIGHT in the second hunk", "RIGHT", 11, ip(10), ip(11), "", false},
		{"removed LEFT", "LEFT", 2, ip(2), nil, "", false},
		{"removed LEFT, second hunk", "LEFT", 11, ip(11), nil, "", false},
		{"unchanged LEFT carries new number", "LEFT", 3, ip(3), ip(4), "", false},
		{"first line", "RIGHT", 1, ip(1), ip(1), "", false},
		{"last line before the no-newline marker", "RIGHT", 13, ip(12), ip(13), "", false},
		{"past the last hunk", "RIGHT", 102, nil, nil, domain.ReasonLineNotInDiff, false},
		{"between hunks RIGHT", "RIGHT", 8, nil, nil, domain.ReasonLineNotInDiff, false},
		{"between hunks LEFT", "LEFT", 7, nil, nil, domain.ReasonLineNotInDiff, false},
		{"old number after the first hunk", "LEFT", 5, nil, nil, domain.ReasonLineNotInDiff, false},
		{"zero line", "RIGHT", 0, nil, nil, domain.ReasonLineNotInDiff, false},
		{"negative line", "LEFT", -1, nil, nil, domain.ReasonLineNotInDiff, false},
		{"unknown side", "MIDDLE", 1, nil, nil, domain.ReasonLineNotInDiff, false},
		{"no stored patch", "RIGHT", 1, nil, nil, domain.ReasonLineNotInDiff, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := f
			if tc.patchNil {
				file.Patch = nil
			}
			gotOld, gotNew, why := domain.PositionFor(file, tc.side, tc.line)
			if why != tc.wantWhy {
				t.Errorf("PositionFor(%q, %d) reason = %q, want %q", tc.side, tc.line, why, tc.wantWhy)
			}
			if diff := cmp.Diff(tc.wantOld, gotOld); diff != "" {
				t.Errorf("PositionFor(%q, %d) old mismatch (-want +got):\n%s", tc.side, tc.line, diff)
			}
			if diff := cmp.Diff(tc.wantNew, gotNew); diff != "" {
				t.Errorf("PositionFor(%q, %d) new mismatch (-want +got):\n%s", tc.side, tc.line, diff)
			}
		})
	}
}

// EC4: the three examples of the spec.
func TestPositionFor_SpecExamples(t *testing.T) {
	patch := "@@ -8,4 +10,6 @@\n a\n b\n+c\n+d\n e\n-removed\n f\n"
	// old 8,9 new 10,11 context; + new 12, 13; e old 10 new 14; removed old 11; f old 12 new 15
	f := domain.File{Path: "x", Patch: &patch}
	if o, n, why := domain.PositionFor(f, "RIGHT", 14); why != "" || *o != 10 || *n != 14 {
		t.Errorf("unchanged RIGHT 14 = %v %v %q, want old 10 new 14", o, n, why)
	}
	if o, n, why := domain.PositionFor(f, "LEFT", 11); why != "" || *o != 11 || n != nil {
		t.Errorf("removed LEFT 11 = %v %v %q, want old 11 only", o, n, why)
	}
	if o, n, why := domain.PositionFor(f, "RIGHT", 12); why != "" || o != nil || *n != 12 {
		t.Errorf("added RIGHT 12 = %v %v %q, want new 12 only", o, n, why)
	}
}

func TestPositionFor_HeaderForms(t *testing.T) {
	tests := []struct {
		name    string
		patch   string
		side    string
		line    int
		wantOld *int
		wantNew *int
		wantWhy string
	}{
		{"new file", "@@ -0,0 +1,2 @@\n+a\n+b\n", "RIGHT", 2, nil, ip(2), ""},
		{"deleted file", "@@ -1,2 +0,0 @@\n-a\n-b\n", "LEFT", 2, ip(2), nil, ""},
		{"count omitted means 1", "@@ -5 +5 @@\n-a\n+b\n", "RIGHT", 5, nil, ip(5), ""},
		{"blank context line trimmed of its space", "@@ -1,3 +1,3 @@\n a\n\n c\n", "RIGHT", 2, ip(2), ip(2), ""},
		{"CRLF patch", "@@ -1,2 +1,2 @@\r\n a\r\n+b\r\n", "RIGHT", 2, nil, ip(2), ""},
		{"lines after the counts are ignored", "@@ -1,1 +1,1 @@\n a\n+x\n", "RIGHT", 2, nil, nil, domain.ReasonLineNotInDiff},
		{"line content that looks like a header", "@@ -1,2 +1,2 @@\n-@@ -9 +9 @@\n+@@ -7 +7 @@\n a\n", "RIGHT", 1, nil, ip(1), ""},
		{"no hunk header", "+a\n+b\n", "RIGHT", 1, nil, nil, domain.ReasonLineNotInDiff},
		{"huge number in header", "@@ -99999999999999999999 +1 @@\n-a\n", "LEFT", 1, nil, nil, domain.ReasonLineNotInDiff},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := domain.File{Path: "x", Patch: &tc.patch}
			o, n, why := domain.PositionFor(f, tc.side, tc.line)
			if why != tc.wantWhy {
				t.Errorf("PositionFor(%q, %d) reason = %q, want %q", tc.side, tc.line, why, tc.wantWhy)
			}
			if diff := cmp.Diff(tc.wantOld, o); diff != "" {
				t.Errorf("old mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantNew, n); diff != "" {
				t.Errorf("new mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// entry is one diff line of a generated patch with the numbers a reference model assigns.
type entry struct {
	kind     byte
	old, new int
}

// genPatch builds a unified diff from fuzz bytes and returns it with its lines: bits 0-1 of a
// byte choose context, add or delete; bit 2 starts a new hunk after a gap of (b>>3)%5+1 lines.
func genPatch(ops []byte) (string, []entry) {
	type hunk struct {
		oldStart, newStart int
		lines              []entry
		text               []string
	}
	var hunks []*hunk
	oldNo, newNo := 1, 1
	var cur *hunk
	for i, b := range ops {
		if cur == nil || (b&4 != 0 && i > 0) {
			gap := int(b>>3)%5 + 1
			if cur != nil {
				oldNo += gap
				newNo += gap
			}
			cur = &hunk{oldStart: oldNo, newStart: newNo}
			hunks = append(hunks, cur)
		}
		switch b & 3 {
		case 0, 3:
			cur.lines = append(cur.lines, entry{' ', oldNo, newNo})
			cur.text = append(cur.text, " c")
			oldNo++
			newNo++
		case 1:
			cur.lines = append(cur.lines, entry{'+', 0, newNo})
			cur.text = append(cur.text, "+a")
			newNo++
		case 2:
			cur.lines = append(cur.lines, entry{'-', oldNo, 0})
			cur.text = append(cur.text, "-d")
			oldNo++
		}
	}
	var sb strings.Builder
	var all []entry
	for _, h := range hunks {
		oc, nc := 0, 0
		for _, e := range h.lines {
			if e.kind != '+' {
				oc++
			}
			if e.kind != '-' {
				nc++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", h.oldStart, oc, h.newStart, nc)
		for _, l := range h.text {
			sb.WriteString(l + "\n")
		}
		all = append(all, h.lines...)
	}
	return sb.String(), all
}

// wantPosition is the reference: the first generated line that carries the number on the side.
func wantPosition(lines []entry, right bool, line int) (o, n *int, found bool) {
	for _, e := range lines {
		switch {
		case right && e.new == line && e.new != 0, !right && e.old == line && e.old != 0:
			if e.old != 0 {
				o = ip(e.old)
			}
			if e.new != 0 {
				n = ip(e.new)
			}
			return o, n, true
		}
	}
	return nil, nil, false
}

// FuzzPositionForGenerated checks PositionFor against an independent model on well-formed
// patches: every line number of the file, on both sides, maps exactly as the model says.
func FuzzPositionForGenerated(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{1, 1, 1})
	f.Add([]byte{2, 2, 4, 0, 12, 1})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 400 {
			ops = ops[:400]
		}
		patch, lines := genPatch(ops)
		file := domain.File{Path: "f", Patch: &patch}
		for line := 0; line <= 2*len(ops)+12; line++ {
			for _, right := range []bool{true, false} {
				side := "LEFT"
				if right {
					side = "RIGHT"
				}
				wo, wn, found := wantPosition(lines, right, line)
				o, n, why := domain.PositionFor(file, side, line)
				if found != (why == "") {
					t.Fatalf("PositionFor(%s, %d) reason = %q, found in model = %v\npatch:\n%s", side, line, why, found, patch)
				}
				if diff := cmp.Diff(wo, o); diff != "" {
					t.Fatalf("PositionFor(%s, %d) old mismatch (-want +got):\n%s\npatch:\n%s", side, line, diff, patch)
				}
				if diff := cmp.Diff(wn, n); diff != "" {
					t.Fatalf("PositionFor(%s, %d) new mismatch (-want +got):\n%s\npatch:\n%s", side, line, diff, patch)
				}
			}
		}
	})
}

// FuzzPositionFor feeds arbitrary text: no panic, and a hit always satisfies the requested number.
func FuzzPositionFor(f *testing.F) {
	f.Add(samplePatch, true, 3)
	f.Add("@@ -1 +1 @@\n-a\n+b", false, 1)
	f.Add("@@ -1,2 +1,2 @@\r\n a\r\n", true, 1)
	f.Add("", true, 1)
	f.Fuzz(func(t *testing.T, patch string, right bool, line int) {
		side := "LEFT"
		if right {
			side = "RIGHT"
		}
		o, n, why := domain.PositionFor(domain.File{Path: "f", Patch: &patch}, side, line)
		if why != "" {
			if why != domain.ReasonLineNotInDiff || o != nil || n != nil {
				t.Fatalf("PositionFor miss = %v %v %q, want nil nil %q", o, n, why, domain.ReasonLineNotInDiff)
			}
			return
		}
		if right && (n == nil || *n != line) || !right && (o == nil || *o != line) {
			t.Fatalf("PositionFor(%s, %d) = old %v new %v: the requested side does not carry the line", side, line, o, n)
		}
	})
}

// NFR8: a 3,000-line patch is mapped in well under the 50 ms budget.
func TestPositionFor_LargePatch(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("@@ -1,3000 +1,3000 @@\n")
	for range 3000 {
		sb.WriteString(" line of code\n")
	}
	patch := sb.String()
	start := time.Now()
	o, n, why := domain.PositionFor(domain.File{Path: "f", Patch: &patch}, "RIGHT", 3000)
	if why != "" || *o != 3000 || *n != 3000 {
		t.Fatalf("PositionFor(3000) = %v %v %q", o, n, why)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("PositionFor on 3000 lines took %v, want <= 50ms", d)
	}
}

// FR10: the whole table, plus the neighbours that must not match a row.
func TestGitLabFailureClassify(t *testing.T) {
	const (
		failed = domain.CodeGitLabCommentFailed
		denied = domain.CodeGitLabPermissionDenied
	)
	tests := []struct {
		name       string
		f          domain.GitLabFailure
		wantCode   string
		wantReason string
	}{
		{"401", domain.GitLabFailure{Status: 401}, denied, domain.ReasonTokenRejected},
		{"401 on create", domain.GitLabFailure{Status: 401, Create: true}, denied, domain.ReasonTokenRejected},
		{"403 insufficient scope", domain.GitLabFailure{Status: 403, InsufficientScope: true, Create: true}, denied, domain.ReasonReadOnlyToken},
		{"403 other", domain.GitLabFailure{Status: 403, Create: true}, denied, domain.ReasonForbidden},
		{"404", domain.GitLabFailure{Status: 404}, failed, domain.ReasonNotFound},
		{"404 on create", domain.GitLabFailure{Status: 404, Create: true}, failed, domain.ReasonNotFound},
		{"400 on create", domain.GitLabFailure{Status: 400, Create: true}, failed, domain.ReasonPositionRejected},
		{"422 on create", domain.GitLabFailure{Status: 422, Create: true}, failed, domain.ReasonPositionRejected},
		{"500 on create", domain.GitLabFailure{Status: 500, Create: true}, failed, domain.ReasonPositionRejected},
		{"400 on a read", domain.GitLabFailure{Status: 400}, failed, domain.ReasonRejected},
		{"409", domain.GitLabFailure{Status: 409, Create: true}, failed, domain.ReasonRejected},
		{"429", domain.GitLabFailure{Status: 429, Create: true}, domain.CodeGitLabRateLimited, domain.ReasonRateLimited},
		{"429 on a read", domain.GitLabFailure{Status: 429}, domain.CodeGitLabRateLimited, domain.ReasonRateLimited},
		{"302", domain.GitLabFailure{Status: 302, Create: true}, failed, domain.ReasonRedirect},
		{"301 on a read", domain.GitLabFailure{Status: 301}, failed, domain.ReasonRedirect},
		{"500 on a read", domain.GitLabFailure{Status: 500}, failed, domain.ReasonUnavailable},
		{"503 on create", domain.GitLabFailure{Status: 503, Create: true}, failed, domain.ReasonUnavailable},
		{"timeout or connection error", domain.GitLabFailure{Create: true}, failed, domain.ReasonUnavailable},
		{"non-JSON 2xx", domain.GitLabFailure{Status: 200, NonJSON: true}, failed, domain.ReasonUnavailable},
		{"non-JSON 201 on create", domain.GitLabFailure{Status: 201, NonJSON: true, Create: true}, failed, domain.ReasonUnavailable},
		{"a success status never reads as accepted", domain.GitLabFailure{Status: 200}, failed, domain.ReasonUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, reason := tc.f.Classify()
			if code != tc.wantCode || reason != tc.wantReason {
				t.Errorf("%+v.Classify() = %q, %q, want %q, %q", tc.f, code, reason, tc.wantCode, tc.wantReason)
			}
		})
	}
}
