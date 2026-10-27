package gitlab_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

func TestMergeRequest_DiffRefs(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  gitlab.DiffRefs
	}{
		{"complete", map[string]any{"diff_refs": map[string]any{"base_sha": "b1", "start_sha": "s1", "head_sha": "h1"}},
			gitlab.DiffRefs{BaseSHA: "b1", StartSHA: "s1", HeadSHA: "h1"}},
		{"partial", map[string]any{"diff_refs": map[string]any{"base_sha": "b1", "start_sha": "", "head_sha": nil}},
			gitlab.DiffRefs{BaseSHA: "b1"}},
		{"null", map[string]any{"diff_refs": nil}, gitlab.DiffRefs{}},
		{"absent", nil, gitlab.DiffRefs{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, okJSON(mrJSON(5, "opened", tc.extra)))
			got, err := s.client(gitlab.Options{}).MergeRequest(t.Context(), s.conn(), 42, 5)
			if err != nil {
				t.Fatalf("MergeRequest() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got.DiffRefs); diff != "" {
				t.Errorf("MergeRequest().DiffRefs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDiffs_OldPath(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"old_path": "old.go", "new_path": "new.go", "renamed_file": true, "diff": "@@ -1 +1 @@\n-a\n+b\n"},
		map[string]any{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1 @@\n-a\n+b\n"},
		map[string]any{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -9 +9 @@\n-q\n"},
		map[string]any{"old_path": "gone.txt", "new_path": "gone.txt", "deleted_file": true, "diff": "@@ -1 +0,0 @@\n-x\n"},
		map[string]any{"new_path": "fresh.go", "new_file": true, "diff": "@@ -0,0 +1 @@\n+x\n"},
	}))
	files, err := s.client(gitlab.Options{}).Diffs(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("Diffs() error = %v", err)
	}
	type row struct{ Path, OldPath string }
	var got []row
	for _, f := range files {
		got = append(got, row{f.Path, f.OldPath})
	}
	want := []row{{"new.go", "old.go"}, {"a.go", "a.go"}, {"gone.txt", "gone.txt"}, {"fresh.go", ""}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Diffs() paths mismatch (-want +got):\n%s", diff)
	}
}

func TestVersions(t *testing.T) {
	s := newStub(t, okJSON([]any{map[string]any{
		"id": 9, "head_commit_sha": "h", "base_commit_sha": "b", "start_commit_sha": "s",
	}}))
	got, err := s.client(gitlab.Options{}).Versions(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("Versions() error = %v", err)
	}
	if diff := cmp.Diff([]gitlab.Version{{ID: 9, BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}}, got); diff != "" {
		t.Errorf("Versions() mismatch (-want +got):\n%s", diff)
	}
	if uri := s.req(0).URI; uri != "/api/v4/projects/42/merge_requests/5/versions?page=1&per_page=1" {
		t.Errorf("request URI = %q", uri)
	}
}

func TestVersions_EmptyAndErrors(t *testing.T) {
	s := newStub(t, okJSON([]any{}))
	got, err := s.client(gitlab.Options{}).Versions(t.Context(), s.conn(), 42, 5)
	if err != nil || len(got) != 0 {
		t.Errorf("Versions() = %v, %v; want no entries and no error", got, err)
	}
	if _, err := s.client(gitlab.Options{}).Versions(t.Context(), s.conn(), 0, 5); err == nil {
		t.Error("Versions(project 0) succeeded")
	}
	if _, err := s.client(gitlab.Options{}).Versions(t.Context(), s.conn(), 1, 0); err == nil {
		t.Error("Versions(iid 0) succeeded")
	}
}

func TestMergedMergeRequests(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"iid": 3, "title": "Add x", "merged_at": "2026-10-20T08:00:00.123Z", "author": map[string]any{"username": "bob"}},
		map[string]any{"iid": 2, "title": "Fix y", "merged_at": nil, "author": nil},
	}))
	got, err := s.client(gitlab.Options{}).MergedMergeRequests(t.Context(), s.conn(), 42, 30)
	if err != nil {
		t.Fatalf("MergedMergeRequests() error = %v", err)
	}
	want := []gitlab.MergedMergeRequest{
		{IID: 3, Title: "Add x", Author: "bob", MergedAt: "2026-10-20T08:00:00.123Z"},
		{IID: 2, Title: "Fix y", Author: "unknown"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MergedMergeRequests() mismatch (-want +got):\n%s", diff)
	}
	if uri := s.req(0).URI; uri != "/api/v4/projects/42/merge_requests?order_by=updated_at&page=1&per_page=30&sort=desc&state=merged" {
		t.Errorf("request URI = %q", uri)
	}
}

func TestMergedMergeRequests_Errors(t *testing.T) {
	s := newStub(t, okJSON([]any{map[string]any{"title": "no iid"}}))
	c := s.client(gitlab.Options{})
	if _, err := c.MergedMergeRequests(t.Context(), s.conn(), 42, 30); !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("MergedMergeRequests() error = %v, want ErrNotAPI", err)
	}
	n := s.count()
	for _, tc := range []struct {
		pid     int64
		perPage int
	}{{0, 30}, {42, 0}, {42, 101}} {
		if _, err := c.MergedMergeRequests(t.Context(), s.conn(), tc.pid, tc.perPage); err == nil {
			t.Errorf("MergedMergeRequests(%d, %d) succeeded", tc.pid, tc.perPage)
		}
	}
	if s.count() != n {
		t.Errorf("refused input sent %d requests", s.count()-n)
	}
}

func TestDiffPaths(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"old_path": "a.go", "new_path": "a.go"},
		map[string]any{"old_path": "gone.txt", "new_path": "gone.txt", "deleted_file": true},
		map[string]any{"old_path": "old.go", "new_path": "new.go", "renamed_file": true},
		map[string]any{"old_path": "a.go", "new_path": "a.go"},
		map[string]any{"old_path": "gone2.txt", "new_path": "", "deleted_file": true},
		map[string]any{"old_path": "", "new_path": ""},
	}))
	got, err := s.client(gitlab.Options{}).DiffPaths(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if diff := cmp.Diff([]string{"a.go", "gone.txt", "new.go", "gone2.txt"}, got); diff != "" {
		t.Errorf("DiffPaths() mismatch (-want +got):\n%s", diff)
	}
	if s.count() != 1 || s.req(0).URI != "/api/v4/projects/42/merge_requests/5/diffs?page=1&per_page=100" {
		t.Errorf("requests = %d, first %q; want exactly page 1 at 100 per page", s.count(), s.req(0).URI)
	}
}

func TestDiffPaths_ReadsOnlyPageOne(t *testing.T) {
	s := newStub(t, okJSON(diffEntries(1, 100))) // a full page: Diffs would ask for page 2
	got, err := s.client(gitlab.Options{}).DiffPaths(t.Context(), s.conn(), 42, 5)
	if err != nil || len(got) != 100 {
		t.Fatalf("DiffPaths() = %d paths, %v; want 100", len(got), err)
	}
	if s.count() != 1 {
		t.Errorf("%d requests, want 1", s.count())
	}
}

func rawStub(t *testing.T, status int, body string, ct string) *stub {
	t.Helper()
	return newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestRawFile(t *testing.T) {
	tests := []struct {
		name, project, file, ref, wantURI string
	}{
		{"numeric id", "42", "docs/a b.md", "abc123",
			"/api/v4/projects/42/repository/files/docs%2Fa%20b.md/raw?ref=abc123"},
		{"project path", "group/sub/name", "README.md", "feature/x y",
			"/api/v4/projects/group%2Fsub%2Fname/repository/files/README.md/raw?ref=feature%2Fx+y"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := rawStub(t, 200, "# hello\n\x00bin", "text/plain; charset=utf-8")
			got, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), tc.project, tc.file, tc.ref, 65536)
			if err != nil {
				t.Fatalf("RawFile() error = %v", err)
			}
			if string(got) != "# hello\n\x00bin" {
				t.Errorf("RawFile() = %q", got)
			}
			r := s.req(0)
			if r.Method != "GET" || r.URI != tc.wantURI {
				t.Errorf("request = %s %s, want GET %s", r.Method, r.URI, tc.wantURI)
			}
			if r.Header.Get("PRIVATE-TOKEN") != testToken || strings.Contains(r.URI, testToken) {
				t.Errorf("token must travel in PRIVATE-TOKEN only")
			}
		})
	}
}

func TestRawFile_SizeLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{"exactly the limit", 100, nil},
		{"one byte over", 101, gitlab.ErrTooLarge},
		{"empty file", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := rawStub(t, 200, strings.Repeat("x", tc.size), "text/plain")
			got, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "f.md", "main", 100)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RawFile() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && got != nil {
				t.Errorf("RawFile() returned %d bytes with ErrTooLarge, want none", len(got))
			}
			if tc.wantErr == nil && len(got) != tc.size {
				t.Errorf("RawFile() = %d bytes, want %d", len(got), tc.size)
			}
			if s.count() != 1 {
				t.Errorf("%d requests, want 1 (ErrTooLarge is not retried)", s.count())
			}
		})
	}
}

func TestRawFile_OnlyReadsLimitPlusOne(t *testing.T) {
	var written atomic.Int64
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 64<<10))
		for range 512 { // 32 MiB offered
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil || r.Context().Err() != nil {
				return
			}
		}
	})
	_, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "big.bin", "main", 1024)
	if !errors.Is(err, gitlab.ErrTooLarge) {
		t.Fatalf("RawFile() error = %v, want ErrTooLarge", err)
	}
	if w := written.Load(); w >= 32<<20 {
		t.Errorf("server wrote the whole %d bytes; the client should stop reading near the limit", w)
	}
}

func TestRawFile_Failures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body, ct   string
		wantStatus int
		wantErr    error
	}{
		{"404 json", 404, `{"message":"404 File Not Found"}`, "application/json", 404, nil},
		{"404 html", 404, `<html><body>secret page</body></html>`, "text/html", 404, nil},
		{"401", 401, `{"message":"401 Unauthorized"}`, "application/json", 401, nil},
		{"302", 302, ``, "", 0, gitlab.ErrRedirect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := rawStub(t, tc.status, tc.body, tc.ct)
			_, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "f.md", "main", 100)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("RawFile() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			var se *gitlab.StatusError
			if !errors.As(err, &se) || se.Status != tc.wantStatus {
				t.Fatalf("RawFile() error = %v, want *StatusError %d", err, tc.wantStatus)
			}
			if strings.Contains(err.Error(), "secret page") || strings.Contains(err.Error(), "<html>") {
				t.Errorf("error %q echoes an HTML body", err)
			}
			if tc.status == 404 && !se.IsNotFound() {
				t.Error("404 is not IsNotFound()")
			}
			if s.count() != 1 {
				t.Errorf("%d requests for a %d, want 1", s.count(), tc.status)
			}
		})
	}
}

func TestRawFile_RetriesTransientFailures(t *testing.T) {
	var n atomic.Int32
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			writeJSON(w, 503, map[string]any{})
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	got, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "f.md", "main", 100)
	if err != nil || string(got) != "ok" {
		t.Fatalf("RawFile() = %q, %v; want ok", got, err)
	}
	if s.count() != 3 {
		t.Errorf("%d requests, want 3", s.count())
	}
	s2 := rawStub(t, 503, `{}`, "application/json")
	if _, err := s2.client(gitlab.Options{Retry: &retry.Options{}}).RawFile(t.Context(), s2.conn(), "g/p", "f.md", "main", 100); err == nil || s2.count() != 1 {
		t.Errorf("zero retry policy: error %v, %d requests; want an error and 1 request", err, s2.count())
	}
}

func TestRawFile_RefusesBadInputWithoutRequest(t *testing.T) {
	s := rawStub(t, 200, "x", "text/plain")
	c := s.client(gitlab.Options{})
	tests := []struct {
		name, project, file, ref string
		limit                    int
	}{
		{"empty project", "", "f", "main", 10},
		{"project with ..", "g/../p", "f", "main", 10},
		{"project with empty segment", "g//p", "f", "main", 10},
		{"project with backslash", `g\p`, "f", "main", 10},
		{"empty file", "g/p", "", "main", 10},
		{"file ..", "g/p", "..", "main", 10},
		{"file with ..", "g/p", "a/../../b", "main", 10},
		{"file leading slash", "g/p", "/etc/passwd", "main", 10},
		{"file with newline", "g/p", "a\nb", "main", 10},
		{"empty ref", "g/p", "f", "", 10},
		{"ref with newline", "g/p", "f", "ma\nin", 10},
		{"limit 0", "g/p", "f", "main", 0},
		{"limit negative", "g/p", "f", "main", -1},
	}
	for _, tc := range tests {
		if _, err := c.RawFile(t.Context(), s.conn(), tc.project, tc.file, tc.ref, tc.limit); err == nil {
			t.Errorf("RawFile(%s) succeeded, want an error", tc.name)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests for refused input, want 0", n)
	}
}

func TestRawFile_RefIsOnlyAQueryValue(t *testing.T) {
	s := rawStub(t, 200, "x", "text/plain")
	ref := "main&private_token=1#frag/../x"
	if _, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "f", ref, 10); err != nil {
		t.Fatalf("RawFile() error = %v", err)
	}
	uri := s.req(0).URI
	if !strings.HasSuffix(uri, "/raw?ref=main%26private_token%3D1%23frag%2F..%2Fx") {
		t.Errorf("request URI = %q; the ref must be one escaped query value", uri)
	}
}

func TestRawFile_ErrorsCarryNoToken(t *testing.T) {
	s := rawStub(t, 403, fmt.Sprintf(`{"message":"bad %s"}`, testToken), "application/json")
	_, err := s.client(gitlab.Options{}).RawFile(t.Context(), s.conn(), "g/p", "f", "main", 10)
	if err == nil {
		t.Fatal("RawFile() succeeded")
	}
	assertNoToken(t, err.Error())
}
