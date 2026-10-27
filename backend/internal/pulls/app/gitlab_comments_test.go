package app_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/gitlab/notebody"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

const (
	cWebURL   = "https://git.corp.example/acme/team/api"
	cHead     = "1111111111111111111111111111111111111111"
	cBase     = "2222222222222222222222222222222222222222"
	cStart    = "3333333333333333333333333333333333333333"
	cOtherSHA = "4444444444444444444444444444444444444444"
	cToken    = "glpat-secret-token"
	cDiscID   = "abcdef0123456789abcdef0123456789abcdef01"
)

// cPatch has an added line (new 3), a removed line (old 7) and an unchanged line (old 10 / new 12).
var cPatch = strings.Join([]string{
	"@@ -1,3 +1,4 @@",
	" a",
	" b",
	"+c",
	" d",
	"@@ -7,4 +10,3 @@",
	"-r",
	" y",
	" z",
	" w",
}, "\n")

// fakeComments is a hand-written GitLabComments: scripted answers, every call recorded.
type fakeComments struct {
	mu sync.Mutex

	readyErr error
	pages    [][]domain.Discussion // by page - 1; more = a later page is listed
	endless  bool                  // every page past pages is empty with more = true
	discErr  error

	refs       domain.DiffRefs
	refsErr    error
	version    domain.DiffRefs
	versionOK  bool
	versionErr error

	created   domain.Discussion
	createErr error
	note      domain.Note
	noteErr   error

	calls      []string // method names in order
	creates    []createCall
	notes      []noteCall
	discPages  []int
	discNumber []int
}

type createCall struct {
	Number int
	Body   string
	Pos    domain.Position
}

type noteCall struct {
	Number       int
	DiscussionID string
	Body         string
}

func (f *fakeComments) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

// requests are the recorded calls that would be GitLab requests (Ready sends none).
func (f *fakeComments) requests() []string {
	var out []string
	for _, c := range f.calls {
		if c != "Ready" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeComments) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *fakeComments) Ready(context.Context, domain.Repo) error {
	f.record("Ready")
	return f.readyErr
}

func (f *fakeComments) Discussions(_ context.Context, _ domain.Repo, number, page int) ([]domain.Discussion, bool, error) {
	f.record("Discussions")
	f.mu.Lock()
	f.discPages = append(f.discPages, page)
	f.discNumber = append(f.discNumber, number)
	f.mu.Unlock()
	if f.discErr != nil {
		return nil, false, f.discErr
	}
	if page > len(f.pages) {
		return nil, f.endless, nil
	}
	return f.pages[page-1], page < len(f.pages) || f.endless, nil
}

func (f *fakeComments) MergeRequestRefs(context.Context, domain.Repo, int) (domain.DiffRefs, error) {
	f.record("MergeRequestRefs")
	return f.refs, f.refsErr
}

func (f *fakeComments) LatestVersion(context.Context, domain.Repo, int) (domain.DiffRefs, bool, error) {
	f.record("LatestVersion")
	return f.version, f.versionOK, f.versionErr
}

func (f *fakeComments) CreateDiscussion(_ context.Context, _ domain.Repo, number int, body string, pos domain.Position) (domain.Discussion, error) {
	f.record("CreateDiscussion")
	f.mu.Lock()
	f.creates = append(f.creates, createCall{Number: number, Body: body, Pos: pos})
	f.mu.Unlock()
	return f.created, f.createErr
}

func (f *fakeComments) CreateNote(_ context.Context, _ domain.Repo, number int, discussionID, body string) (domain.Note, error) {
	f.record("CreateNote")
	f.mu.Lock()
	f.notes = append(f.notes, noteCall{Number: number, DiscussionID: discussionID, Body: body})
	f.mu.Unlock()
	return f.note, f.noteErr
}

// cFixture is a GitLab merge request !7 with a stored diff of two files and a fake GitLab.
type cFixture struct {
	st   *fakeStore
	glc  *fakeComments
	gh   *fakeGitHub
	svc  *app.Service
	logs *logSink
	pr   domain.Pull
	repo domain.Repo
}

func newCFixture(t *testing.T) *cFixture {
	t.Helper()
	repo := glRepo
	repo.WebURL = cWebURL
	head := cHead
	pr := glPull(7, domain.DiffStats{})
	pr.HeadSHA = head
	pr.DiffHead = &head

	st := newStore()
	st.repos[glRepoID] = repo
	st.pulls = []domain.Pull{pr}
	st.files[pr.ID] = []domain.File{
		{Path: "src/a.go", Patch: new(cPatch)},
		{Path: "new/a.ts", PreviousPath: new("old/a.ts"), Patch: new(cPatch)},
		{Path: "big.bin"}, // no stored patch
		{Path: "odd.go", Patch: new("@@ -0,2 +1,2 @@\n x\n y")}, // a context line numbered old 0
	}
	glc := &fakeComments{
		refs:    domain.DiffRefs{BaseSHA: cBase, StartSHA: cStart, HeadSHA: cHead},
		created: domain.Discussion{ID: cDiscID, Notes: []domain.Note{{ID: 501, Body: "stored", Author: "bot", CreatedAt: now}}},
		note:    domain.Note{ID: 502, Body: "stored reply", Author: "bot", CreatedAt: now},
	}
	gh := &fakeGitHub{}
	logs := &logSink{}
	svc := app.NewService(st, gh, slog.New(logs), func() time.Time { return now },
		app.WithGitLabComments(glc),
		app.WithRedact(func(s string) string { return strings.ReplaceAll(s, cToken, "[redacted]") }))
	return &cFixture{st: st, glc: glc, gh: gh, svc: svc, logs: logs, pr: pr, repo: repo}
}

func (f *cFixture) post(t *testing.T, req app.CommentRequest) (domain.ReviewComment, error) {
	t.Helper()
	return f.svc.PostComment(t.Context(), wsID, f.pr.ID, req)
}

func textNote(id int64, user string, pos *domain.Position) domain.Note {
	return domain.Note{ID: id, Type: "DiffNote", Body: "n" + string(rune('0'+id%10)), Author: user, CreatedAt: now, Position: pos}
}

func textPos(head string, oldLine, newLine *int) *domain.Position {
	return &domain.Position{Type: "text", HeadSHA: head, OldPath: "src/a.go", NewPath: "src/a.go", OldLine: oldLine, NewLine: newLine}
}

func errParts(t *testing.T, err error) (apperr.Kind, *apperr.Error, map[string]any) {
	t.Helper()
	kind, ae := kindOf(t, err)
	details, _ := ae.Details.(map[string]any)
	return kind, ae, details
}

// ---- reading ----

// AC1, EC1, FR2, FR3.
func TestComments_gitlabMapsTextDiffThreads(t *testing.T) {
	f := newCFixture(t)
	f.glc.pages = [][]domain.Discussion{{
		{ID: "sys", Notes: []domain.Note{{ID: 1, System: true, Body: "added 1 commit"}}},
		{ID: "gen", Notes: []domain.Note{{ID: 2, Type: "DiscussionNote", Body: "general", Author: "bob"}}},
		{ID: "img", Notes: []domain.Note{textNote(3, "bob", &domain.Position{Type: "image", HeadSHA: cHead, NewPath: "x.png"})}},
		{ID: "file", Notes: []domain.Note{textNote(4, "bob", &domain.Position{Type: "file", HeadSHA: cHead, NewPath: "x.go"})}},
		{ID: "diff", Notes: []domain.Note{
			textNote(11, "ann", textPos(cHead, nil, new(5))),
			textNote(12, "bob", textPos(cHead, nil, new(5))),
			{ID: 13, Type: "DiscussionNote", Body: "third", CreatedAt: now},
		}},
	}}

	got, err := f.svc.Comments(t.Context(), wsID, f.pr.ID)
	if err != nil {
		t.Fatalf("Comments() error = %v", err)
	}
	url := func(id string) string { return cWebURL + "/-/merge_requests/7#note_" + id }
	want := []domain.ReviewComment{
		{ID: 11, Path: "src/a.go", Side: "RIGHT", Line: new(5), OriginalLine: new(5), Body: "n1", User: "ann", CreatedAt: now, HTMLURL: url("11")},
		{ID: 12, Path: "src/a.go", Side: "RIGHT", Line: new(5), OriginalLine: new(5), Body: "n2", User: "bob", CreatedAt: now, HTMLURL: url("12"), InReplyToID: new(int64(11))},
		{ID: 13, Path: "src/a.go", Side: "RIGHT", Line: new(5), OriginalLine: new(5), Body: "third", User: "unknown", CreatedAt: now, HTMLURL: url("13"), InReplyToID: new(int64(11))},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Comments() mismatch (-want +got):\n%s", diff)
	}
	if f.gh.listCalls != 0 || f.glc.count("Discussions") != 1 {
		t.Errorf("GitHub list calls = %d, Discussions calls = %d, want 0 and 1", f.gh.listCalls, f.glc.count("Discussions"))
	}
	if diff := cmp.Diff([]int{7}, f.glc.discNumber); diff != "" {
		t.Errorf("Discussions MR numbers mismatch (-want +got):\n%s", diff)
	}
}

// AC2, EC2, FR2: at most three pages, the first 100 comments, stop on !more.
func TestComments_gitlabPagingBounds(t *testing.T) {
	diffDisc := func(id int64) domain.Discussion {
		return domain.Discussion{ID: "d", Notes: []domain.Note{textNote(id, "ann", textPos(cHead, nil, new(1)))}}
	}
	general := domain.Discussion{ID: "g", Notes: []domain.Note{{ID: 9999, Type: "DiscussionNote", Body: "x"}}}
	// 4 pages of 100 discussions; every third one is a text diff note.
	var pages [][]domain.Discussion
	var next int64 = 1
	for range 4 {
		var page []domain.Discussion
		for i := range 100 {
			if i%3 == 0 {
				page = append(page, diffDisc(next))
				next++
			} else {
				page = append(page, general)
			}
		}
		pages = append(pages, page)
	}
	full := make([]domain.Discussion, 100)
	for i := range full {
		full[i] = diffDisc(int64(i + 1))
	}

	tests := []struct {
		name      string
		pages     [][]domain.Discussion
		wantPages []int
		wantLen   int
	}{
		{"three pages even though a fourth exists", pages, []int{1, 2, 3}, 100},
		{"a full page of comments stops at once", [][]domain.Discussion{full, full}, []int{1}, 100},
		{"the last page stops", pages[:2], []int{1, 2}, 68},
		{"no discussions", nil, []int{1}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			f.glc.pages = tc.pages
			got, err := f.svc.Comments(t.Context(), wsID, f.pr.ID)
			if err != nil || len(got) != tc.wantLen {
				t.Fatalf("Comments() = %d comments, %v, want %d", len(got), err, tc.wantLen)
			}
			if diff := cmp.Diff(tc.wantPages, f.glc.discPages); diff != "" {
				t.Errorf("Discussions pages mismatch (-want +got):\n%s", diff)
			}
			for i, c := range got {
				if c.ID != int64(i+1) {
					t.Fatalf("comment %d has id %d, want GitLab order", i, c.ID)
				}
			}
		})
	}
}

// AC3, EC3, FR4: outdated against the diff head, else the stored head sha.
func TestComments_gitlabOutdated(t *testing.T) {
	root := func(head string, oldLine, newLine *int) []domain.Discussion {
		return []domain.Discussion{{ID: "d", Notes: []domain.Note{textNote(21, "ann", textPos(head, oldLine, newLine))}}}
	}
	tests := []struct {
		name     string
		diffHead *string
		headSHA  string
		page     []domain.Discussion
		want     domain.ReviewComment
	}{
		{
			name: "older head is outdated without a line", diffHead: new(cHead), headSHA: cHead,
			page: root(cOtherSHA, nil, new(5)),
			want: domain.ReviewComment{Side: "RIGHT", OriginalLine: new(5), IsOutdated: true},
		},
		{
			name: "left-only root", diffHead: new(cHead), headSHA: cHead,
			page: root(cHead, new(7), nil),
			want: domain.ReviewComment{Side: "LEFT", Line: new(7), OriginalLine: new(7)},
		},
		{
			name: "no diff head compares the stored head", diffHead: nil, headSHA: cOtherSHA,
			page: root(cHead, nil, new(5)),
			want: domain.ReviewComment{Side: "RIGHT", OriginalLine: new(5), IsOutdated: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			f.pr.DiffHead, f.pr.HeadSHA = tc.diffHead, tc.headSHA
			f.st.pulls = []domain.Pull{f.pr}
			f.glc.pages = [][]domain.Discussion{tc.page}
			got, err := f.svc.Comments(t.Context(), wsID, f.pr.ID)
			if err != nil || len(got) != 1 {
				t.Fatalf("Comments() = %v, %v, want one comment", got, err)
			}
			c := got[0]
			c.ID, c.Path, c.Body, c.User, c.CreatedAt, c.HTMLURL = 0, "", "", "", time.Time{}, ""
			if diff := cmp.Diff(tc.want, c); diff != "" {
				t.Errorf("Comments() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// AC4, EC16, FR1: every failure is an empty list; not connected / no token send nothing.
func TestComments_gitlabAnswersNothingOnFailure(t *testing.T) {
	tests := []struct {
		name         string
		readyErr     error
		discErr      error
		wantRequests int
		wantWarns    int
	}{
		{"not connected", app.ErrNotConnected, nil, 0, 0},
		{"no token", &app.NoTokenError{Conn: "corp"}, nil, 0, 0},
		{"status 500", nil, &app.GitLabError{Status: 500, Message: "boom"}, 1, 1},
		{"unexpected body", nil, app.ErrGitLabUnavailable, 1, 1},
		{"timeout", nil, context.DeadlineExceeded, 1, 1},
		{"redirect", nil, app.ErrGitLabRedirect, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			f.glc.readyErr, f.glc.discErr = tc.readyErr, tc.discErr
			got, err := f.svc.Comments(t.Context(), wsID, f.pr.ID)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("Comments() = %#v, %v, want an empty non-nil list", got, err)
			}
			if n := len(f.glc.requests()); n != tc.wantRequests {
				t.Errorf("GitLab requests = %d, want %d", n, tc.wantRequests)
			}
			if n := len(f.logs.warns()); n != tc.wantWarns {
				t.Errorf("WARN lines = %v, want %d", f.logs.warns(), tc.wantWarns)
			}
			if f.gh.listCalls != 0 {
				t.Errorf("GitHub list calls = %d, want 0", f.gh.listCalls)
			}
		})
	}
}

func TestComments_gitlabLogRedactsTheError(t *testing.T) {
	f := newCFixture(t)
	f.glc.discErr = &app.GitLabError{Status: 500, Message: "token " + cToken}
	if _, err := f.svc.Comments(t.Context(), wsID, f.pr.ID); err != nil {
		t.Fatalf("Comments() error = %v", err)
	}
	for _, attrs := range f.logs.warnAttrs() {
		for k, v := range attrs {
			if strings.Contains(v, cToken) {
				t.Errorf("WARN attribute %s = %q holds the token", k, v)
			}
		}
	}
}

func TestComments_gitlabWithoutThePortIsNotConnected(t *testing.T) {
	st := newStore()
	repo := glRepo
	st.repos[glRepoID] = repo
	pr := glPull(7, domain.DiffStats{})
	st.pulls = []domain.Pull{pr}
	gh := &fakeGitHub{comments: []domain.ReviewComment{{ID: 1}}}
	svc := app.NewService(st, gh, glDiscardLog(), func() time.Time { return now })

	got, err := svc.Comments(t.Context(), wsID, pr.ID)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Comments() = %#v, %v, want an empty list", got, err)
	}
	_, err = svc.PostComment(t.Context(), wsID, pr.ID, app.CommentRequest{Path: "a.go", Line: 1, Body: "x"})
	kind, ae := kindOf(t, err)
	if kind != apperr.KindValidation || ae.Code != "repo_host_not_connected" {
		t.Errorf("PostComment() = %v %q, want 422 repo_host_not_connected", kind, ae.Code)
	}
	if len(gh.createCalls) != 0 {
		t.Errorf("GitHub create calls = %d, want 0", len(gh.createCalls))
	}
}

// ---- posting a new comment ----

// AC5, EC4, FR6–FR8: positions of an unchanged, a removed and an added line; two requests per post.
func TestPostComment_gitlabPositionsFromTheStoredPatch(t *testing.T) {
	tests := []struct {
		name             string
		side             string
		line             int
		wantOld, wantNew *int
	}{
		{"unchanged line, RIGHT default", "", 12, new(10), new(12)},
		{"removed line, LEFT", "LEFT", 7, new(7), nil},
		{"added line, RIGHT", "RIGHT", 3, nil, new(3)},
		{"unchanged line, LEFT", "LEFT", 10, new(10), new(12)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			got, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: tc.line, Side: tc.side, Body: "hello"})
			if err != nil {
				t.Fatalf("PostComment() error = %v", err)
			}
			wantPos := domain.Position{
				Type: "text", BaseSHA: cBase, StartSHA: cStart, HeadSHA: cHead,
				OldPath: "src/a.go", NewPath: "src/a.go", OldLine: tc.wantOld, NewLine: tc.wantNew,
			}
			want := []createCall{{Number: 7, Body: "hello", Pos: wantPos}}
			if diff := cmp.Diff(want, f.glc.creates); diff != "" {
				t.Errorf("CreateDiscussion calls mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{"MergeRequestRefs", "CreateDiscussion"}, f.glc.requests()); diff != "" {
				t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
			}
			side := tc.side
			if side == "" {
				side = "RIGHT"
			}
			wantC := domain.ReviewComment{
				ID: 501, Path: "src/a.go", Side: side, Line: new(tc.line), OriginalLine: new(tc.line), Body: "stored",
				User: "bot", CreatedAt: now, HTMLURL: cWebURL + "/-/merge_requests/7#note_501",
			}
			if diff := cmp.Diff(wantC, got); diff != "" {
				t.Errorf("PostComment() mismatch (-want +got):\n%s", diff)
			}
			if f.gh.createdOwner != "" {
				t.Errorf("a GitHub comment was created")
			}
		})
	}
}

// AC6, EC8, FR8: a renamed file keeps its previous path as old_path.
func TestPostComment_gitlabRenamedFile(t *testing.T) {
	f := newCFixture(t)
	if _, err := f.post(t, app.CommentRequest{Path: "new/a.ts", Line: 3, Body: "x"}); err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	pos := f.glc.creates[0].Pos
	if pos.OldPath != "old/a.ts" || pos.NewPath != "new/a.ts" {
		t.Errorf("position paths = old %q new %q, want old/a.ts and new/a.ts", pos.OldPath, pos.NewPath)
	}
}

// AC7, EC5, FR6: nothing is sent when the position cannot be found.
func TestPostComment_gitlabLineAndFileNotInDiff(t *testing.T) {
	tests := []struct {
		name    string
		req     app.CommentRequest
		reason  string
		message string
	}{
		{"line between hunks", app.CommentRequest{Path: "src/a.go", Line: 5, Body: "x"}, "line_not_in_diff", "Line 5 (RIGHT) is not part of the diff of src/a.go."},
		{"left line between hunks", app.CommentRequest{Path: "src/a.go", Line: 5, Side: "LEFT", Body: "x"}, "line_not_in_diff", "Line 5 (LEFT) is not part of the diff of src/a.go."},
		{"file without a stored patch", app.CommentRequest{Path: "big.bin", Line: 1, Body: "x"}, "line_not_in_diff", "Line 1 (RIGHT) is not part of the diff of big.bin."},
		{"matched line with an unusable old number", app.CommentRequest{Path: "odd.go", Line: 1, Body: "x"}, "line_not_in_diff", "Line 1 (RIGHT) is not part of the diff of odd.go."},
		{"path not in the diff", app.CommentRequest{Path: "nope.go", Line: 1, Body: "x"}, "file_not_in_diff", "nope.go is not part of this merge request's diff."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			_, err := f.post(t, tc.req)
			kind, ae, details := errParts(t, err)
			if kind != apperr.KindInvalidInput || ae.Code != "gitlab_comment_failed" || ae.Message != tc.message {
				t.Errorf("PostComment() = %v %q %q, want 400 gitlab_comment_failed %q", kind, ae.Code, ae.Message, tc.message)
			}
			if diff := cmp.Diff(map[string]any{"reason": tc.reason, "cause": ""}, details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
			if got := f.glc.requests(); len(got) != 0 {
				t.Errorf("GitLab requests = %v, want none", got)
			}
		})
	}
}

// AC8, EC6, EC7, FR7.
func TestPostComment_gitlabDiffRefs(t *testing.T) {
	versionRefs := domain.DiffRefs{BaseSHA: "b2", StartSHA: "s2", HeadSHA: cHead}
	tests := []struct {
		name         string
		mod          func(f *cFixture)
		wantReason   string // "" = posted
		wantMessage  string
		wantRequests []string
		wantRefs     domain.DiffRefs
	}{
		{
			name: "refs and versions both empty",
			mod: func(f *cFixture) {
				f.glc.refs = domain.DiffRefs{}
			},
			wantReason:   "diff_not_ready",
			wantMessage:  "GitLab is still preparing this merge request's diff. Try again in a minute.",
			wantRequests: []string{"MergeRequestRefs", "LatestVersion"},
		},
		{
			name: "incomplete refs fall back to the newest version",
			mod: func(f *cFixture) {
				f.glc.refs = domain.DiffRefs{HeadSHA: cHead}
				f.glc.version, f.glc.versionOK = versionRefs, true
			},
			wantRequests: []string{"MergeRequestRefs", "LatestVersion", "CreateDiscussion"},
			wantRefs:     versionRefs,
		},
		{
			name: "incomplete version is not ready",
			mod: func(f *cFixture) {
				f.glc.refs = domain.DiffRefs{}
				f.glc.version, f.glc.versionOK = domain.DiffRefs{HeadSHA: cHead}, true
			},
			wantReason:   "diff_not_ready",
			wantMessage:  "GitLab is still preparing this merge request's diff. Try again in a minute.",
			wantRequests: []string{"MergeRequestRefs", "LatestVersion"},
		},
		{
			name: "pushed since the diff was loaded",
			mod: func(f *cFixture) {
				f.glc.refs.HeadSHA = cOtherSHA
			},
			wantReason:   "diff_changed",
			wantMessage:  "The merge request changed since this diff was loaded. Reload the PR and post again.",
			wantRequests: []string{"MergeRequestRefs"},
		},
		{
			name: "no diff head stored",
			mod: func(f *cFixture) {
				f.pr.DiffHead = nil
				f.st.pulls = []domain.Pull{f.pr}
			},
			wantReason:   "diff_changed",
			wantMessage:  "The merge request changed since this diff was loaded. Reload the PR and post again.",
			wantRequests: []string{"MergeRequestRefs"},
		},
		{
			name: "version head differs from the diff head",
			mod: func(f *cFixture) {
				f.glc.refs = domain.DiffRefs{}
				f.glc.version, f.glc.versionOK = domain.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: cOtherSHA}, true
			},
			wantReason:   "diff_changed",
			wantMessage:  "The merge request changed since this diff was loaded. Reload the PR and post again.",
			wantRequests: []string{"MergeRequestRefs", "LatestVersion"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			tc.mod(f)
			_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
			if tc.wantReason == "" {
				if err != nil {
					t.Fatalf("PostComment() error = %v", err)
				}
				if got := f.glc.creates[0].Pos; got.BaseSHA != tc.wantRefs.BaseSHA || got.StartSHA != tc.wantRefs.StartSHA || got.HeadSHA != tc.wantRefs.HeadSHA {
					t.Errorf("position SHAs = %s/%s/%s, want the version's %+v", got.BaseSHA, got.StartSHA, got.HeadSHA, tc.wantRefs)
				}
			} else {
				kind, ae, details := errParts(t, err)
				if kind != apperr.KindInvalidInput || ae.Code != "gitlab_comment_failed" || ae.Message != tc.wantMessage || details["reason"] != tc.wantReason {
					t.Errorf("PostComment() = %v %q %q %v, want 400 gitlab_comment_failed %s", kind, ae.Code, ae.Message, details, tc.wantReason)
				}
				if len(f.glc.creates) != 0 {
					t.Errorf("CreateDiscussion calls = %d, want none", len(f.glc.creates))
				}
			}
			if diff := cmp.Diff(tc.wantRequests, f.glc.requests()); diff != "" {
				t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// AC15, EC17: a merged or closed MR still accepts the post.
func TestPostComment_gitlabMergedMergeRequest(t *testing.T) {
	f := newCFixture(t)
	f.pr.Status = "merged"
	f.st.pulls = []domain.Pull{f.pr}
	if _, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"}); err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	if len(f.glc.creates) != 1 {
		t.Errorf("CreateDiscussion calls = %d, want 1", len(f.glc.creates))
	}
}

func TestPostComment_gitlabEmptyCreateAnswerIsNotConfirmed(t *testing.T) {
	f := newCFixture(t)
	f.glc.created = domain.Discussion{ID: cDiscID}
	_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
	_, _, details := errParts(t, err)
	if details["reason"] != "unavailable" || len(f.glc.creates) != 1 {
		t.Errorf("PostComment() details = %v, creates = %d, want unavailable after one create", details, len(f.glc.creates))
	}
}

// ---- replies ----

func replyPages(noteDiscPage int, target int64) [][]domain.Discussion {
	var pages [][]domain.Discussion
	for p := 1; p <= 5; p++ {
		page := []domain.Discussion{{ID: "aaaaaaaa", Notes: []domain.Note{textNote(int64(100+p), "ann", textPos(cHead, nil, new(1)))}}}
		if p == noteDiscPage {
			page = append(page, domain.Discussion{ID: cDiscID, Notes: []domain.Note{
				textNote(300, "ann", textPos(cHead, nil, new(8))),
				textNote(target, "bob", textPos(cHead, nil, new(8))),
			}})
		}
		pages = append(pages, page)
	}
	return pages
}

// AC9, EC13, FR9.
func TestPostComment_gitlabReplyFindsTheDiscussion(t *testing.T) {
	f := newCFixture(t)
	f.glc.pages = replyPages(4, 310)

	got, err := f.post(t, app.CommentRequest{Path: "ignored", Line: 99, Side: "LEFT", Body: "- /merge", InReplyTo: new(int64(310))})
	if err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	if diff := cmp.Diff([]int{1, 2, 3, 4}, f.glc.discPages); diff != "" {
		t.Errorf("Discussions pages mismatch (-want +got):\n%s", diff)
	}
	wantNote := []noteCall{{Number: 7, DiscussionID: cDiscID, Body: "- " + notebody.ZWSP + "/merge"}}
	if diff := cmp.Diff(wantNote, f.glc.notes); diff != "" {
		t.Errorf("CreateNote calls mismatch (-want +got):\n%s", diff)
	}
	if f.glc.count("CreateDiscussion")+f.glc.count("MergeRequestRefs")+f.glc.count("LatestVersion") != 0 {
		t.Errorf("GitLab requests = %v, want only the discussion pages and the note", f.glc.requests())
	}
	want := domain.ReviewComment{
		ID: 502, Path: "src/a.go", Side: "RIGHT", Line: new(8), OriginalLine: new(8), Body: "stored reply", User: "bot",
		CreatedAt: now, HTMLURL: cWebURL + "/-/merge_requests/7#note_502", InReplyToID: new(int64(300)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PostComment() mismatch (-want +got):\n%s", diff)
	}
}

func TestPostComment_gitlabReplyTargetNotFound(t *testing.T) {
	systemPage := []domain.Discussion{{ID: cDiscID, Notes: []domain.Note{
		textNote(300, "ann", textPos(cHead, nil, new(8))),
		{ID: 310, System: true, Body: "changed"},
	}}}
	tests := []struct {
		name         string
		fixture      func(f *cFixture)
		target       int64
		wantRequests int
	}{
		{"deleted note stops after ten pages", func(f *cFixture) { f.glc.endless = true }, 999, 10},
		{"the last page stops the search", func(f *cFixture) { f.glc.pages = replyPages(0, 0)[:2] }, 999, 2},
		{"system note", func(f *cFixture) { f.glc.pages = [][]domain.Discussion{systemPage} }, 310, 1},
		{"non-positive id sends nothing", func(f *cFixture) {}, 0, 0},
		{"discussion id that is not hex", func(f *cFixture) {
			f.glc.pages = [][]domain.Discussion{{{ID: "../x", Notes: []domain.Note{textNote(310, "a", textPos(cHead, nil, new(1)))}}}}
		}, 310, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			tc.fixture(f)
			_, err := f.post(t, app.CommentRequest{Path: "a", Line: 1, Body: "x", InReplyTo: &tc.target})
			kind, ae, details := errParts(t, err)
			if kind != apperr.KindInvalidInput || ae.Code != "gitlab_comment_failed" ||
				ae.Message != "The comment you are replying to was not found on GitLab." || details["reason"] != "reply_target_not_found" {
				t.Errorf("PostComment() = %v %q %q %v, want 400 reply_target_not_found", kind, ae.Code, ae.Message, details)
			}
			if got := f.glc.count("Discussions"); got != tc.wantRequests {
				t.Errorf("Discussions calls = %d, want %d", got, tc.wantRequests)
			}
			if len(f.glc.notes) != 0 {
				t.Errorf("CreateNote calls = %d, want none", len(f.glc.notes))
			}
		})
	}
}

func TestPostComment_gitlabReplyToAThreadWithoutAPosition(t *testing.T) {
	f := newCFixture(t)
	f.glc.pages = [][]domain.Discussion{{{ID: cDiscID, Notes: []domain.Note{{ID: 40, Type: "DiscussionNote", Body: "general"}}}}}
	got, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 4, Body: "x", InReplyTo: new(int64(40))})
	if err != nil {
		t.Fatalf("PostComment() error = %v", err)
	}
	if got.ID != 502 || got.InReplyToID == nil || *got.InReplyToID != 40 || got.IsOutdated {
		t.Errorf("PostComment() = %+v, want note 502 replying to 40", got)
	}
}

// ---- errors ----

// AC10, EC9–EC12, FR10, FR11: the create is sent once and every answer maps to its error.
func TestPostComment_gitlabCreateErrors(t *testing.T) {
	const host = "git.corp.example"
	tests := []struct {
		name    string
		err     error
		code    string
		reason  string
		message string
		cause   string
		retry   int
	}{
		{"401", &app.GitLabError{Status: 401, Message: "401 Unauthorized"}, "gitlab_permission_denied", "token_rejected",
			"GitLab rejected the token of connection " + host + " (401).", "401 Unauthorized", 0},
		{"403 insufficient scope", &app.GitLabError{Status: 403, Message: "insufficient_scope", InsufficientScope: true}, "gitlab_permission_denied", "read_only_token",
			"The token of connection " + host + " cannot post: it needs the api scope.", "insufficient_scope", 0},
		{"403 insufficient scope by message", &app.GitLabError{Status: 403, Message: "insufficient_scope"}, "gitlab_permission_denied", "read_only_token",
			"The token of connection " + host + " cannot post: it needs the api scope.", "insufficient_scope", 0},
		{"401 names the connection", &app.GitLabError{Status: 401, Message: "401 Unauthorized", Conn: "Corp GitLab"}, "gitlab_permission_denied", "token_rejected",
			"GitLab rejected the token of connection Corp GitLab (401).", "401 Unauthorized", 0},
		{"403 insufficient scope names the connection", &app.GitLabError{Status: 403, Message: "insufficient_scope", InsufficientScope: true, Conn: "Corp GitLab"}, "gitlab_permission_denied", "read_only_token",
			"The token of connection Corp GitLab cannot post: it needs the api scope.", "insufficient_scope", 0},
		{"403 other", &app.GitLabError{Status: 403, Message: "locked"}, "gitlab_permission_denied", "forbidden",
			"GitLab refused the comment (403): the token's user may lack the Reporter role, or the discussion is locked.", "locked", 0},
		{"404", &app.GitLabError{Status: 404, Message: "404 Not found"}, "gitlab_comment_failed", "not_found",
			"GitLab did not find this merge request, or the token cannot see it.", "404 Not found", 0},
		{"400 line_code", &app.GitLabError{Status: 400, Message: "Note {:line_code=>[\"can't be blank\"]}"}, "gitlab_comment_failed", "position_rejected",
			"GitLab could not place the comment on src/a.go line 3. Reload the PR and try again.", "Note {:line_code=>[\"can't be blank\"]}", 0},
		{"422", &app.GitLabError{Status: 422}, "gitlab_comment_failed", "position_rejected",
			"GitLab could not place the comment on src/a.go line 3. Reload the PR and try again.", "GitLab answered HTTP 422", 0},
		{"500", &app.GitLabError{Status: 500, Message: "Internal Server Error"}, "gitlab_comment_failed", "position_rejected",
			"GitLab could not place the comment on src/a.go line 3. Reload the PR and try again.", "Internal Server Error", 0},
		{"409 with a message", &app.GitLabError{Status: 409, Message: "Branch is protected"}, "gitlab_comment_failed", "rejected",
			"Branch is protected", "Branch is protected", 0},
		{"409 without a message", &app.GitLabError{Status: 409}, "gitlab_comment_failed", "rejected",
			"GitLab refused the comment (409).", "GitLab answered HTTP 409", 0},
		{"429 with Retry-After", &app.GitLabError{Status: 429, RateLimited: true, RetryAfterSeconds: 37}, "gitlab_rate_limited", "rate_limited",
			"GitLab limits how many comments can be posted per minute. Try again in 37 s.", "GitLab answered HTTP 429", 37},
		{"429 without Retry-After", &app.GitLabError{Status: 429, RateLimited: true}, "gitlab_rate_limited", "rate_limited",
			"GitLab limits how many comments can be posted per minute. Try again in 60 s.", "GitLab answered HTTP 429", 60},
		{"429 with a Retry-After time only", &app.GitLabError{Status: 429, RetryAfter: now.Add(2500 * time.Millisecond)}, "gitlab_rate_limited", "rate_limited",
			"GitLab limits how many comments can be posted per minute. Try again in 3 s.", "GitLab answered HTTP 429", 3},
		{"redirect", app.ErrGitLabRedirect, "gitlab_comment_failed", "redirect",
			"GitLab answered with a redirect — check the connection's base URL.", "redirect", 0},
		{"503", &app.GitLabError{Status: 503, Message: "Service Unavailable"}, "gitlab_comment_failed", "unavailable",
			"GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again.", "Service Unavailable", 0},
		{"non-JSON answer", app.ErrGitLabUnavailable, "gitlab_comment_failed", "unavailable",
			"GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again.", "unavailable", 0},
		{"timeout", context.DeadlineExceeded, "gitlab_comment_failed", "unavailable",
			"GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again.", "timeout", 0},
		{"unknown error class", errors.New("dial tcp 10.0.0.5: refused"), "gitlab_comment_failed", "unavailable",
			"GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again.", "*errors.errorString", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			f.glc.createErr = tc.err
			_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
			kind, ae, details := errParts(t, err)
			if kind != apperr.KindInvalidInput || ae.Code != tc.code || ae.Message != tc.message {
				t.Errorf("PostComment() = %v %q %q, want 400 %s %q", kind, ae.Code, ae.Message, tc.code, tc.message)
			}
			want := map[string]any{"reason": tc.reason, "cause": tc.cause}
			if tc.retry > 0 {
				want["retry_after_s"] = tc.retry
			}
			if diff := cmp.Diff(want, details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
			if len(f.glc.creates) != 1 {
				t.Errorf("CreateDiscussion calls = %d, want exactly 1 (never retried)", len(f.glc.creates))
			}
		})
	}
}

// FR10: reads of the post flow use the same table; position_rejected is for the create only.
func TestPostComment_gitlabReadErrors(t *testing.T) {
	tests := []struct {
		name   string
		mod    func(f *cFixture)
		req    app.CommentRequest
		reason string
	}{
		{"merge request 404", func(f *cFixture) { f.glc.refsErr = &app.GitLabError{Status: 404} }, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"}, "not_found"},
		{"merge request 400 is rejected", func(f *cFixture) { f.glc.refsErr = &app.GitLabError{Status: 400, Message: "bad"} }, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"}, "rejected"},
		{"merge request 500 is unavailable", func(f *cFixture) { f.glc.refsErr = &app.GitLabError{Status: 500} }, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"}, "unavailable"},
		{"versions 403", func(f *cFixture) {
			f.glc.refs = domain.DiffRefs{}
			f.glc.versionErr = &app.GitLabError{Status: 403}
		}, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"}, "forbidden"},
		{"discussion list 401", func(f *cFixture) { f.glc.discErr = &app.GitLabError{Status: 401} }, app.CommentRequest{Path: "a", Line: 1, Body: "x", InReplyTo: new(int64(5))}, "token_rejected"},
		{"reply 400 is rejected", func(f *cFixture) {
			f.glc.pages = replyPages(1, 310)
			f.glc.noteErr = &app.GitLabError{Status: 400, Message: "Cannot reply to a system note"}
		}, app.CommentRequest{Path: "a", Line: 1, Body: "x", InReplyTo: new(int64(310))}, "rejected"},
		{"reply 500 is unavailable", func(f *cFixture) {
			f.glc.pages = replyPages(1, 310)
			f.glc.noteErr = &app.GitLabError{Status: 500}
		}, app.CommentRequest{Path: "a", Line: 1, Body: "x", InReplyTo: new(int64(310))}, "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			tc.mod(f)
			_, err := f.post(t, tc.req)
			_, _, details := errParts(t, err)
			if details["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", details, tc.reason)
			}
			if len(f.glc.creates) > 1 || len(f.glc.notes) > 1 {
				t.Errorf("POSTs = %d discussions, %d notes, want at most one", len(f.glc.creates), len(f.glc.notes))
			}
		})
	}
}

// NFR4, K3: tokens are removed before the cut; rejected messages are short; no raw error text.
func TestPostComment_gitlabRedactsBeforeCutting(t *testing.T) {
	t.Run("token across the 1000-byte cut", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.createErr = &app.GitLabError{Status: 400, Message: strings.Repeat("a", 988) + cToken + " tail"}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		_, _, details := errParts(t, err)
		cause, _ := details["cause"].(string)
		if strings.Contains(cause, "glpat") || len(cause) > 1000 {
			t.Errorf("cause ends %q (%d bytes), want the token removed and at most 1000 bytes", cause[len(cause)-20:], len(cause))
		}
	})
	t.Run("multi-byte cause is cut on a character", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.createErr = &app.GitLabError{Status: 400, Message: strings.Repeat("界", 400)}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		_, _, details := errParts(t, err)
		cause, _ := details["cause"].(string)
		if len(cause) != 999 || !strings.HasSuffix(cause, "界") {
			t.Errorf("cause = %d bytes, want 999 (333 whole characters)", len(cause))
		}
	})
	t.Run("rejected message is redacted and at most 300 characters", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.createErr = &app.GitLabError{Status: 409, Message: cToken + strings.Repeat("界", 400)}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		_, ae, _ := errParts(t, err)
		if strings.Contains(ae.Message, "glpat") || len([]rune(ae.Message)) != 300 {
			t.Errorf("message has %d characters and token %t, want 300 and no token", len([]rune(ae.Message)), strings.Contains(ae.Message, "glpat"))
		}
	})
	t.Run("nothing in the error text carries the token", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.createErr = &app.GitLabError{Status: 500, Message: "boom " + cToken}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		if strings.Contains(err.Error(), "glpat") {
			t.Errorf("error text %q holds the token", err.Error())
		}
		for _, r := range f.logs.infoAttrs() {
			for k, v := range r {
				if strings.Contains(v, "glpat") {
					t.Errorf("log attribute %s = %q holds the token", k, v)
				}
			}
		}
	})
}

// AC11, EC16, FR5: no GitLab request before the connection and token checks.
func TestPostComment_gitlabConnectionChecks(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.readyErr = app.ErrNotConnected
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		kind, ae, _ := errParts(t, err)
		wantDetails := map[string]string{"host": "git.corp.example", "repo_id": glRepoID}
		if kind != apperr.KindValidation || ae.Code != "repo_host_not_connected" ||
			ae.Message != "No GitLab connection for git.corp.example. Add one in Settings → GitLab Integration." {
			t.Errorf("PostComment() = %v %q %q, want 422 repo_host_not_connected", kind, ae.Code, ae.Message)
		}
		if diff := cmp.Diff(wantDetails, ae.Details); diff != "" {
			t.Errorf("details mismatch (-want +got):\n%s", diff)
		}
		if got := f.glc.requests(); len(got) != 0 {
			t.Errorf("GitLab requests = %v, want none", got)
		}
	})
	t.Run("no token", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.readyErr = &app.NoTokenError{Conn: "corp"}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		kind, ae, _ := errParts(t, err)
		if kind != apperr.KindInvalidInput || ae.Code != "gitlab_unavailable" ||
			ae.Message != "Connect a token for git.corp.example in Settings → GitLab Integration to post comments." {
			t.Errorf("PostComment() = %v %q %q, want 400 gitlab_unavailable", kind, ae.Code, ae.Message)
		}
		if got := f.glc.requests(); len(got) != 0 {
			t.Errorf("GitLab requests = %v, want none", got)
		}
	})
	t.Run("connection deleted after the check", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.refsErr = app.ErrNotConnected
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
		kind, ae, _ := errParts(t, err)
		if kind != apperr.KindValidation || ae.Code != "repo_host_not_connected" {
			t.Errorf("PostComment() = %v %q, want 422 repo_host_not_connected", kind, ae.Code)
		}
	})
	t.Run("unknown pull request", func(t *testing.T) {
		f := newCFixture(t)
		_, err := f.svc.PostComment(t.Context(), wsID, "nope", app.CommentRequest{Path: "a", Line: 1, Body: "x"})
		kind, _ := kindOf(t, err)
		if kind != apperr.KindNotFound || len(f.glc.calls) != 0 {
			t.Errorf("PostComment() kind = %v, calls = %v, want 404 and none", kind, f.glc.calls)
		}
	})
}

// AC13, FR12, NFR2: the server applies the note body rules; GitHub bodies stay verbatim.
func TestPostComment_gitlabBodiesPassTheRules(t *testing.T) {
	z := notebody.ZWSP
	finding := "**[high · bug] <b>t</b>**\n\n@ann /approve\n\n<sub>DevDigest · 80% confidence</sub>\n<!-- devdigest-finding:abc -->"
	wantFinding := "**[high · bug] &lt;b&gt;t&lt;/b&gt;**\n\n@" + z + "ann /approve\n\n<sub>DevDigest · 80% confidence</sub>\n<!-- devdigest-finding:abc -->"
	tests := []struct {
		name string
		body string
		want string
	}{
		{"finding", finding, wantFinding},
		{"free text", "/approve\n@ann see <b>x</b>", z + "/approve\n@" + z + "ann see &lt;b&gt;x&lt;/b&gt;"},
		{"CRLF", "a\r\n/close", "a\n" + z + "/close"},
		// An unclosed fence in the rationale would swallow the sub line and the marker as code text, and
		// the duplicate check would then miss the posted comment: the rules close it before them.
		{
			"unclosed fence in the rationale",
			"**[high · bug] t**\n\n```go\nx := a<b\n\n**Suggested fix**\n\nfix\n\n<sub>DevDigest · 80% confidence</sub>\n\n<!-- devdigest-finding:abc -->",
			"**[high · bug] t**\n\n```go\nx := a<" + z + "b\n\n**Suggested fix**\n\nfix\n\n```\n<sub>DevDigest · 80% confidence</sub>\n\n<!-- devdigest-finding:abc -->",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCFixture(t)
			if _, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: tc.body}); err != nil {
				t.Fatalf("PostComment() error = %v", err)
			}
			if got := f.glc.creates[0].Body; got != tc.want {
				t.Errorf("CreateDiscussion body = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("reply", func(t *testing.T) {
		f := newCFixture(t)
		f.glc.pages = replyPages(1, 310)
		if _, err := f.post(t, app.CommentRequest{Path: "a", Line: 1, Body: "- /merge", InReplyTo: new(int64(310))}); err != nil {
			t.Fatalf("PostComment() error = %v", err)
		}
		if got := f.glc.notes[0].Body; got != "- "+z+"/merge" {
			t.Errorf("CreateNote body = %q, want the rule output", got)
		}
	})

	t.Run("GitHub body is forwarded unchanged", func(t *testing.T) {
		st := newStore()
		pr := stored(7, domain.DiffStats{})
		st.pulls = []domain.Pull{pr}
		gh := &fakeGitHub{}
		glc := &fakeComments{}
		svc := app.NewService(st, gh, glDiscardLog(), func() time.Time { return now }, app.WithGitLabComments(glc))
		body := "/approve\n@ann see <b>x</b>"
		if _, err := svc.PostComment(t.Context(), wsID, pr.ID, app.CommentRequest{Path: "a.go", Line: 1, Body: body}); err != nil {
			t.Fatalf("PostComment() error = %v", err)
		}
		if len(gh.createCalls) != 1 || gh.createCalls[0].Body != body {
			t.Errorf("GitHub create calls = %+v, want the body verbatim", gh.createCalls)
		}
		if len(glc.calls) != 0 {
			t.Errorf("GitLab calls = %v, want none for a GitHub repository", glc.calls)
		}
	})
}

// AC14, EC14, FR5, SCR5: the limit is checked on the rule output, in characters.
func TestPostComment_gitlabBodyLength(t *testing.T) {
	t.Run("grows past the limit through inserted characters", func(t *testing.T) {
		f := newCFixture(t)
		body := strings.Repeat("/a\n", 333_330) // 999,990 characters; every line gains one
		if n := notebody.Chars(body); n != 999_990 {
			t.Fatalf("test body has %d characters, want 999990", n)
		}
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: body})
		kind, ae, details := errParts(t, err)
		if kind != apperr.KindInvalidInput || ae.Code != "gitlab_comment_failed" || details["reason"] != "body_too_long" || details["cause"] != "" {
			t.Errorf("PostComment() = %v %q %v, want 400 gitlab_comment_failed body_too_long", kind, ae.Code, details)
		}
		if got := f.glc.requests(); len(got) != 0 {
			t.Errorf("GitLab requests = %v, want none", got)
		}
	})
	t.Run("exactly at the limit in multi-byte characters is accepted", func(t *testing.T) {
		f := newCFixture(t)
		body := strings.Repeat("界", notebody.MaxChars) // 3 MB in bytes, 1,000,000 characters
		if _, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: body}); err != nil {
			t.Fatalf("PostComment() error = %v", err)
		}
		if len(f.glc.creates) != 1 {
			t.Errorf("CreateDiscussion calls = %d, want 1", len(f.glc.creates))
		}
	})
	t.Run("one character over", func(t *testing.T) {
		f := newCFixture(t)
		_, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: strings.Repeat("x", notebody.MaxChars+1)})
		_, _, details := errParts(t, err)
		if details["reason"] != "body_too_long" {
			t.Errorf("details = %v, want body_too_long", details)
		}
	})
}

// AC16, NFR6: one INFO line per action with PR id, operation, code and reason, and no body.
func TestPostComment_gitlabLogsOneInfoLinePerAction(t *testing.T) {
	const secretBody = "unique-body-text"
	f := newCFixture(t)
	f.glc.pages = replyPages(1, 310)

	if _, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: secretBody}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := f.post(t, app.CommentRequest{Path: "a", Line: 1, Body: secretBody, InReplyTo: new(int64(310))}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	f.glc.createErr = &app.GitLabError{Status: 403, Message: "locked"}
	if _, err := f.post(t, app.CommentRequest{Path: "src/a.go", Line: 3, Body: secretBody}); err == nil {
		t.Fatal("PostComment() error = nil, want a 403 failure")
	}

	got := f.logs.infoAttrs()
	want := []map[string]string{
		{"msg": "gitlab comment", "pr_id": f.pr.ID, "operation": "post", "code": "ok", "reason": ""},
		{"msg": "gitlab comment", "pr_id": f.pr.ID, "operation": "reply", "code": "ok", "reason": ""},
		{"msg": "gitlab comment", "pr_id": f.pr.ID, "operation": "post", "code": "gitlab_permission_denied", "reason": "forbidden"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("INFO lines mismatch (-want +got):\n%s", diff)
	}
	if w := f.logs.warns(); len(w) != 0 {
		t.Errorf("WARN lines = %v, want none in the post flow", w)
	}
	for _, r := range got {
		for k, v := range r {
			if strings.Contains(v, secretBody) {
				t.Errorf("log attribute %s = %q holds the body", k, v)
			}
		}
	}
}

// infoAttrs returns the attributes of the INFO records, one map per record, plus "msg".
func (s *logSink) infoAttrs() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]string
	for _, r := range s.recs {
		if r.Level != slog.LevelInfo {
			continue
		}
		m := map[string]string{"msg": r.Message}
		r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.String(); return true })
		out = append(out, m)
	}
	return out
}

func TestWithRedact_nilKeepsTheIdentity(t *testing.T) {
	f := newCFixture(t)
	svc := app.NewService(f.st, f.gh, glDiscardLog(), func() time.Time { return now },
		app.WithGitLabComments(f.glc), app.WithRedact(nil))
	f.glc.createErr = &app.GitLabError{Status: 409, Message: "plain"}
	_, err := svc.PostComment(t.Context(), wsID, f.pr.ID, app.CommentRequest{Path: "src/a.go", Line: 3, Body: "x"})
	_, ae, _ := errParts(t, err)
	if ae.Message != "plain" {
		t.Errorf("message = %q, want plain", ae.Message)
	}
}
