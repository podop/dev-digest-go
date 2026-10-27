package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/adapters/catalog"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

const (
	gitlabToken = "glpat-secret-token"
	corpBase    = "https://git.corp.example/gitlab"
	// ec23URL is the spec's EC23 link: the ref may be "feature" or "feature/x".
	ec23URL = "https://git.corp.example/gitlab/acme/skills/-/blob/feature/x/skills/a.md"
)

type rawCall struct{ Project, File, Ref string }

// fakeGitLab is a gitLabFiles port: the connections it knows by host key and canned answers by
// ref. It never sees a token; the real token lives in the adapter.
type fakeGitLab struct {
	conns    map[string]app.GitLabFileConn
	matchErr error
	matched  []string
	answers  map[string][]byte // by ref; a missing ref answers ErrGitLabFileNotFound
	failWith error             // answers every Raw with this error
	calls    []rawCall
	limits   []int
}

func corpConn() app.GitLabFileConn {
	return app.GitLabFileConn{ID: "c1", Name: "corp", BaseURL: corpBase, Host: "git.corp.example"}
}

func newFakeGitLab(answers map[string][]byte) *fakeGitLab {
	return &fakeGitLab{conns: map[string]app.GitLabFileConn{"git.corp.example": corpConn()}, answers: answers}
}

func (f *fakeGitLab) Match(_ context.Context, hostKey string) (app.GitLabFileConn, bool, error) {
	f.matched = append(f.matched, hostKey)
	if f.matchErr != nil {
		return app.GitLabFileConn{}, false, f.matchErr
	}
	c, ok := f.conns[hostKey]
	return c, ok, nil
}

func (f *fakeGitLab) Raw(_ context.Context, conn app.GitLabFileConn, project, file, ref string, limit int) ([]byte, error) {
	f.calls = append(f.calls, rawCall{project, file, ref})
	f.limits = append(f.limits, limit)
	if f.failWith != nil {
		return nil, f.failWith
	}
	if data, ok := f.answers[ref]; ok {
		return data, nil
	}
	return nil, app.ErrGitLabFileNotFound
}

func gitlabImporter(t *testing.T, f *fakeFetcher, g *fakeGitLab) *app.Importer {
	t.Helper()
	cat, err := catalog.New()
	if err != nil {
		t.Fatalf("catalog.New error = %v", err)
	}
	return app.NewImporter(f, cat).WithGitLabFiles(g)
}

func previewURL(t *testing.T, i *app.Importer, raw string) (app.Preview, error) {
	t.Helper()
	return i.Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: raw})
}

func TestImporter_GitLabConnectionFile_EC23(t *testing.T) {
	g := newFakeGitLab(map[string][]byte{"feature/x": []byte("# Review rules\n\nBe kind.\n")})
	f := &fakeFetcher{}

	got, err := previewURL(t, gitlabImporter(t, f, g), ec23URL)
	if err != nil {
		t.Fatalf("Preview(%q) error = %v", ec23URL, err)
	}
	wantCalls := []rawCall{
		{"acme/skills", "x/skills/a.md", "feature"},
		{"acme/skills", "skills/a.md", "feature/x"},
	}
	if diff := cmp.Diff(wantCalls, g.calls); diff != "" {
		t.Errorf("Raw calls mismatch (-want +got):\n%s", diff)
	}
	for _, l := range g.limits {
		if l != domain.URLImportMaxBytes {
			t.Errorf("Raw limit = %d, want %d", l, domain.URLImportMaxBytes)
		}
	}
	if diff := cmp.Diff([]string{"git.corp.example"}, g.matched); diff != "" {
		t.Errorf("Match keys mismatch (-want +got):\n%s", diff)
	}
	if len(f.urls) != 0 {
		t.Errorf("public fetcher got %v, want no request for a connection URL", f.urls)
	}
	if got.Source != domain.SourceImportedURL || got.SourceRef != ec23URL {
		t.Errorf("Preview source = (%v, %q), want (imported_url, the typed URL)", got.Source, got.SourceRef)
	}
	if got.Name != "a" || got.Body == "" {
		t.Errorf("Preview name/body = (%q, %q), want name a (file stem) and the body", got.Name, got.Body)
	}
	if diff := cmp.Diff([]string{"/skills/a.md"}, got.IncludedFiles); diff != "" {
		t.Errorf("IncludedFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_GitLabConnectionFile_StopsAtTheFirstAnswerThatIsNot404(t *testing.T) {
	g := newFakeGitLab(map[string][]byte{"main": []byte("# Skill\n\nbody\n")})
	raw := "https://git.corp.example/gitlab/acme/skills/-/raw/main/a/b/c.md?ref_type=heads"

	if _, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), raw); err != nil {
		t.Fatalf("Preview(%q) error = %v", raw, err)
	}
	if diff := cmp.Diff([]rawCall{{"acme/skills", "a/b/c.md", "main"}}, g.calls); diff != "" {
		t.Errorf("Raw calls mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_GitLabConnectionFile_AtMostFiveSplits(t *testing.T) {
	g := newFakeGitLab(nil)
	raw := "https://git.corp.example/gitlab/acme/skills/-/blob/a/b/c/d/e/f/g/h.md"

	_, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), raw)

	wantInvalidImport(t, err, "File not found on git.corp.example, or the token cannot see it")
	if len(g.calls) != domain.MaxGitLabRefSplits {
		t.Errorf("Raw called %d times, want %d", len(g.calls), domain.MaxGitLabRefSplits)
	}
}

func TestImporter_GitLabConnectionFile_Zip(t *testing.T) {
	zip := makeZip(t, [2]string{"SKILL.md", "# Zipped\n\nbody\n"})
	g := newFakeGitLab(map[string][]byte{"main": zip})

	got, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), "https://git.corp.example/gitlab/acme/skills/-/raw/main/pack.zip")
	if err != nil {
		t.Fatalf("Preview(zip) error = %v", err)
	}
	if got.Name != "pack" || len(got.IncludedFiles) != 1 || got.IncludedFiles[0] != "SKILL.md" {
		t.Errorf("Preview(zip) = (%q, %v), want the archive's SKILL.md named after pack.zip", got.Name, got.IncludedFiles)
	}
}

func TestImporter_GitLabConnectionFile_NotUTF8(t *testing.T) {
	g := newFakeGitLab(map[string][]byte{"main": {0xff, 0xfe, 0x00}})
	_, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), "https://git.corp.example/gitlab/acme/skills/-/raw/main/a.md")
	wantInvalidImport(t, err, "The file is not valid UTF-8 text")
}

func TestImporter_GitLabConnectionURL_EC25(t *testing.T) {
	const msg = "Only GitLab blob or raw file links can be imported from git.corp.example"
	tests := []struct{ name, url string }{
		{"tree", "https://git.corp.example/gitlab/acme/skills/-/tree/main/skills"},
		{"snippets", "https://git.corp.example/gitlab/acme/skills/-/snippets/1"},
		{"bare project", "https://git.corp.example/gitlab/acme/skills"},
		{"the base itself", "https://git.corp.example/gitlab"},
		{"blob without a file", "https://git.corp.example/gitlab/acme/skills/-/blob/main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitLab(map[string][]byte{"main": []byte("x")})
			f := &fakeFetcher{}
			_, err := previewURL(t, gitlabImporter(t, f, g), tc.url)
			wantInvalidImport(t, err, msg)
			if len(g.calls) != 0 || len(f.urls) != 0 {
				t.Errorf("Preview(%q) sent Raw %v / fetch %v, want nothing", tc.url, g.calls, f.urls)
			}
		})
	}
}

func TestImporter_GitLabConnectionURL_Credentials(t *testing.T) {
	g := newFakeGitLab(nil)
	_, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), "https://user:pw@git.corp.example/gitlab/acme/skills/-/blob/main/a.md")
	wantInvalidImport(t, err, "URLs with credentials are not allowed")
	if len(g.calls) != 0 {
		t.Errorf("Raw calls = %v, want none", g.calls)
	}
}

func TestImporter_GitLabConnectionFile_Failures(t *testing.T) {
	const host = "git.corp.example"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rejected token", app.ErrGitLabFileRejected, "GitLab rejected the token of connection corp"},
		{"moved", app.ErrGitLabFileMoved, "GitLab reports that this project has moved; use its current URL"},
		{"rate limited", app.ErrGitLabFileRateLimited, "GitLab rate limit reached; try again later"},
		{"too large", app.ErrGitLabFileTooLarge, "The document is larger than 1048576 bytes"},
		{"address", app.ErrGitLabFileAddress, host + " resolves to an address that is not allowed"},
		{"connection gone", app.ErrGitLabFileConnectionGone, "GitLab connection corp is no longer available"},
		{"other status", fmt.Errorf("wrapped: %w", &app.GitLabFileStatusError{Status: 502}), "The server answered HTTP 502"},
		{"transport", errors.New("dial tcp 10.0.0.5:443: " + gitlabToken), "Could not fetch " + host},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitLab(nil)
			g.failWith = tc.err
			f := &fakeFetcher{}

			_, err := previewURL(t, gitlabImporter(t, f, g), ec23URL)

			ae := wantAppErr(t, err, apperr.KindValidation, "invalid_import", tc.want)
			if ae != nil && strings.Contains(ae.Message, gitlabToken) {
				t.Errorf("message %q contains the token", ae.Message)
			}
			if len(g.calls) != 1 {
				t.Errorf("Raw called %d times, want 1 (only a 404 tries the next split)", len(g.calls))
			}
			if len(f.urls) != 0 {
				t.Errorf("public fetcher got %v, want nothing", f.urls)
			}
		})
	}
}

func TestImporter_GitLabConnectionFile_Cancel(t *testing.T) {
	g := newFakeGitLab(nil)
	g.failWith = context.Canceled
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := gitlabImporter(t, &fakeFetcher{}, g).Preview(ctx, app.ImportRequest{Kind: app.KindURL, URL: ec23URL})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Preview(canceled) error = %v, want context.Canceled", err)
	}
}

func TestImporter_GitLabMatchFailure(t *testing.T) {
	g := newFakeGitLab(nil)
	g.matchErr = errors.New("db down")
	f := &fakeFetcher{}

	_, err := previewURL(t, gitlabImporter(t, f, g), ec23URL)

	if err == nil || !errors.Is(err, g.matchErr) {
		t.Errorf("Preview() error = %v, want the match failure", err)
	}
	if _, isApp := apperr.As(err); isApp {
		t.Errorf("Preview() error = %v, want a plain server error, not a 422", err)
	}
	if len(f.urls) != 0 {
		t.Errorf("public fetcher got %v after a failed match, want nothing", f.urls)
	}
}

func TestImporter_GitLabNoConnection_TakesTheExistingPath(t *testing.T) {
	doc := domain.FetchedDocument{Bytes: []byte("# Skill\n\nbody\n")}
	tests := []struct {
		name, url, wantFetch string
	}{
		{"EC23 URL without a connection", ec23URL, "https://git.corp.example/gitlab/acme/skills/-/raw/feature/x/skills/a.md"},
		{"AC27 gitlab.com blob (FR31)", "https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads", "https://gitlab.com/acme/skills/-/raw/main/a.md"},
		{"plain file", "https://example.com/skills/a.md", "https://example.com/skills/a.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitLab(nil)
			g.conns = map[string]app.GitLabFileConn{}
			f := &fakeFetcher{doc: doc}

			got, err := previewURL(t, gitlabImporter(t, f, g), tc.url)
			if err != nil {
				t.Fatalf("Preview(%q) error = %v", tc.url, err)
			}
			if diff := cmp.Diff([]string{tc.wantFetch}, f.urls); diff != "" {
				t.Errorf("public fetch mismatch (-want +got):\n%s", diff)
			}
			if len(g.calls) != 0 {
				t.Errorf("Raw calls = %v, want none without a connection", g.calls)
			}
			if got.SourceRef != tc.url {
				t.Errorf("SourceRef = %q, want the typed URL", got.SourceRef)
			}
		})
	}
}

func TestImporter_GitLabNoConnection_PrivateAddressStillRefused(t *testing.T) {
	g := newFakeGitLab(nil)
	g.conns = map[string]app.GitLabFileConn{}
	f := &fakeFetcher{}

	_, err := previewURL(t, gitlabImporter(t, f, g), "https://10.0.0.5/gitlab/acme/skills/-/blob/main/a.md")

	if err == nil {
		t.Fatal("Preview() error = nil, want the non-public address refusal")
	}
	if len(f.urls) != 0 || len(g.calls) != 0 {
		t.Errorf("fetch %v / Raw %v, want none", f.urls, g.calls)
	}
}

func TestImporter_GitLabHostMatchedButOtherPathPrefix_TakesThePublicPath(t *testing.T) {
	// The connection is https://git.corp.example/gitlab; a URL under another prefix of the same
	// host is not the connection's, so no token-bearing call is made for it (NFR3).
	g := newFakeGitLab(map[string][]byte{"main": []byte("x")})
	f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: []byte("# S\n\nb\n")}}

	_, err := previewURL(t, gitlabImporter(t, f, g), "https://git.corp.example/other/acme/skills/-/blob/main/a.md")
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(g.calls) != 0 {
		t.Errorf("Raw calls = %v, want none for another path prefix", g.calls)
	}
	if diff := cmp.Diff([]string{"https://git.corp.example/other/acme/skills/-/raw/main/a.md"}, f.urls); diff != "" {
		t.Errorf("public fetch mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_GitLabHostKeyIncludesANonDefaultPort(t *testing.T) {
	g := newFakeGitLab(map[string][]byte{"main": []byte("# S\n\nb\n")})
	g.conns = map[string]app.GitLabFileConn{"git.corp.example:8443": {
		ID: "c2", Name: "alt", BaseURL: "https://git.corp.example:8443", Host: "git.corp.example:8443",
	}}

	if _, err := previewURL(t, gitlabImporter(t, &fakeFetcher{}, g), "https://Git.Corp.Example:8443/acme/skills/-/blob/main/a.md"); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if diff := cmp.Diff([]string{"git.corp.example:8443"}, g.matched); diff != "" {
		t.Errorf("Match keys mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_GitLabPortIsNotConsultedForOtherKindsOrBadURLs(t *testing.T) {
	g := newFakeGitLab(nil)
	i := gitlabImporter(t, &fakeFetcher{}, g)

	for _, raw := range []string{"http://git.corp.example/gitlab/a/b/-/blob/main/a.md", "not a url", ""} {
		_, err := previewURL(t, i, raw)
		if err == nil {
			t.Errorf("Preview(%q) error = nil, want a 422", raw)
		}
	}
	if len(g.matched) != 0 {
		t.Errorf("Match called for %v, want no lookup for a non-https or unparsable URL", g.matched)
	}
}

func TestImporter_WithoutGitLabFiles_IsToday(t *testing.T) {
	f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: []byte("# S\n\nb\n")}}
	if _, err := previewURL(t, newImporter(t, f), "https://gitlab.com/acme/skills/-/blob/main/a.md"); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if diff := cmp.Diff([]string{"https://gitlab.com/acme/skills/-/raw/main/a.md"}, f.urls); diff != "" {
		t.Errorf("public fetch mismatch (-want +got):\n%s", diff)
	}
}
