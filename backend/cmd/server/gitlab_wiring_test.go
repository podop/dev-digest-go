package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	gitlabconnapp "dev-digest-go/backend/internal/gitlabconn/app"
	gitlabconndomain "dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/secrets"
	pullsdomain "dev-digest-go/backend/internal/pulls/domain"
	reposapp "dev-digest-go/backend/internal/repos/app"
	settingsapp "dev-digest-go/backend/internal/settings/app"
)

// Unit tests of the GitLab A adapters in gitlab.go. The wiring through the real server is covered
// by the component tests of the GitLab slice.

func TestHostName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"gitlab.com", "gitlab.com"},
		{"git.corp.example:8443", "git.corp.example"},
		{"[::1]:8443", "[::1]"},
		{"[::1]", "[::1]"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := hostName(tc.in); got != tc.want {
			t.Errorf("hostName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fakeConns is a connectionResolver over a fixed list.
type fakeConns struct {
	all []gitlabconnapp.Conn
	err error
}

func (f fakeConns) Resolve(_ context.Context, host string) (gitlabconnapp.Conn, bool, error) {
	if f.err != nil {
		return gitlabconnapp.Conn{}, false, f.err
	}
	for _, c := range f.all {
		if c.Host == host {
			return c, true, nil
		}
	}
	return gitlabconnapp.Conn{}, false, nil
}

func (f fakeConns) All(context.Context) ([]gitlabconnapp.Conn, error) { return f.all, f.err }

func TestReposConnections(t *testing.T) {
	conns := fakeConns{all: []gitlabconnapp.Conn{
		{ID: "1", Name: "Cloud", BaseURL: "https://gitlab.com", Host: "gitlab.com", Token: "glpat-aaaaaaaaaaaaaaaaaaaa"},
		{ID: "2", Name: "Corp", BaseURL: "https://git.corp.example:8443", Host: "git.corp.example:8443"},
		{ID: "3", Name: "Corp2", BaseURL: "https://git.corp.example", Host: "git.corp.example"},
	}}
	r := reposConnections{conns: conns}

	t.Run("Resolve carries the token", func(t *testing.T) {
		got, ok, err := r.Resolve(t.Context(), "gitlab.com")
		want := reposapp.GitLabConn{ID: "1", Name: "Cloud", BaseURL: "https://gitlab.com", Host: "gitlab.com", Token: "glpat-aaaaaaaaaaaaaaaaaaaa"}
		if err != nil || !ok || got != want {
			t.Errorf("Resolve(gitlab.com) = %v, %v, %v, want %v, true, nil", got, ok, err, want)
		}
	})
	t.Run("Resolve of an unknown host is not ok", func(t *testing.T) {
		if _, ok, err := r.Resolve(t.Context(), "other.example"); ok || err != nil {
			t.Errorf("Resolve(other.example) = ok %v, err %v, want false, nil", ok, err)
		}
	})
	t.Run("Resolve passes the error", func(t *testing.T) {
		boom := errors.New("db down")
		if _, _, err := (reposConnections{conns: fakeConns{err: boom}}).Resolve(t.Context(), "gitlab.com"); !errors.Is(err, boom) {
			t.Errorf("Resolve() error = %v, want %v", err, boom)
		}
	})
	t.Run("HostKeysByName ignores the port and the case", func(t *testing.T) {
		got, err := r.HostKeysByName(t.Context(), "GIT.corp.example")
		if err != nil {
			t.Fatalf("HostKeysByName() error = %v", err)
		}
		slices.Sort(got)
		if diff := cmp.Diff([]string{"git.corp.example", "git.corp.example:8443"}, got); diff != "" {
			t.Errorf("HostKeysByName(GIT.corp.example) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("HostKeysByName of an unknown name is empty", func(t *testing.T) {
		got, err := r.HostKeysByName(t.Context(), "nope.example")
		if err != nil || len(got) != 0 {
			t.Errorf("HostKeysByName(nope.example) = %v, %v, want none", got, err)
		}
	})
}

func TestPullsResolver_ReadsTheConnectionAtEveryCall(t *testing.T) {
	conns := &mutableConns{c: gitlabconnapp.Conn{ID: "1", Host: "gitlab.com", BaseURL: "https://gitlab.com", Token: "old"}}
	res := pullsResolver(conns)
	got, ok, err := res.Resolve(t.Context(), "gitlab.com")
	if err != nil || !ok || got.Token != "old" || got.BaseURL != "https://gitlab.com" {
		t.Fatalf("Resolve() = %+v, %v, %v, want the old token", got, ok, err)
	}
	conns.c.Token = "new"
	if got, _, _ := res.Resolve(t.Context(), "gitlab.com"); got.Token != "new" {
		t.Errorf("Resolve() after a rotation = token %q, want new", got.Token)
	}
	if _, ok, err := res.Resolve(t.Context(), "other.example"); ok || err != nil {
		t.Errorf("Resolve(other.example) = ok %v, err %v, want false, nil", ok, err)
	}
}

type mutableConns struct{ c gitlabconnapp.Conn }

func (m *mutableConns) Resolve(_ context.Context, host string) (gitlabconnapp.Conn, bool, error) {
	return m.c, host == m.c.Host, nil
}

func (m *mutableConns) All(context.Context) ([]gitlabconnapp.Conn, error) {
	return []gitlabconnapp.Conn{m.c}, nil
}

// fakeTester is the connection test of the gitlabconn use case.
type fakeTester struct {
	res gitlabconnapp.TestResult
	err error
}

func (f fakeTester) TestConnection(context.Context, string, string) (gitlabconnapp.TestResult, error) {
	return f.res, f.err
}

func TestSettingsGitLab_TestConnection(t *testing.T) {
	ms := 12
	login, version, expires := "ada", "17.5.1-ee", "2027-01-02"
	t.Run("a passed test carries every field", func(t *testing.T) {
		res := gitlabconnapp.TestResult{
			OK: true, Message: "Connected as @ada", LatencyMS: &ms, Login: &login, Scopes: []string{"api"},
			ExpiresAt: &expires, InstanceVersion: &version, TokenAccess: gitlabconndomain.AccessFull,
			Warnings: []gitlabconndomain.Warning{{Code: "expires_soon", Message: "Token expires in 3 days"}},
		}
		got, err := settingsGitLab{svc: fakeTester{res: res}}.TestConnection(t.Context(), "id", "")
		access := "full"
		want := settingsapp.ConnTestResult{
			OK: true, Message: "Connected as @ada", LatencyMS: &ms, Login: &login, Scopes: []string{"api"},
			ExpiresAt: &expires, InstanceVersion: &version, TokenAccess: &access,
			Warnings: []settingsapp.TestWarning{{Code: "expires_soon", Message: "Token expires in 3 days"}},
		}
		if err != nil {
			t.Fatalf("TestConnection() error = %v", err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("TestConnection() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("a passed test without warnings has an empty list", func(t *testing.T) {
		got, err := settingsGitLab{svc: fakeTester{res: gitlabconnapp.TestResult{OK: true}}}.TestConnection(t.Context(), "id", "")
		if err != nil || got.Warnings == nil || len(got.Warnings) != 0 || got.TokenAccess != nil {
			t.Errorf("TestConnection() = %+v, %v, want empty non-nil warnings and no token access", got, err)
		}
	})
	t.Run("a failed test is data", func(t *testing.T) {
		got, err := settingsGitLab{svc: fakeTester{res: gitlabconnapp.TestResult{Message: "GitLab rejected the token (401): it is wrong, expired or revoked"}}}.TestConnection(t.Context(), "id", "")
		want := settingsapp.ConnTestResult{Message: "GitLab rejected the token (401): it is wrong, expired or revoked"}
		if err != nil {
			t.Fatalf("TestConnection() error = %v", err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("TestConnection() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("an app error passes through", func(t *testing.T) {
		in := apperr.NotFound("GitLab connection x not found").WithCode(gitlabconnapp.CodeNotFound)
		_, err := settingsGitLab{svc: fakeTester{err: in}}.TestConnection(t.Context(), "x", "")
		if !errors.Is(err, in) {
			t.Errorf("TestConnection() error = %v, want %v", err, in)
		}
	})
	t.Run("a bare ErrNotFound is the 404 gitlab_connection_not_found", func(t *testing.T) {
		_, err := settingsGitLab{svc: fakeTester{err: gitlabconndomain.ErrNotFound}}.TestConnection(t.Context(), "x", "")
		ae, ok := apperr.As(err)
		if !ok || ae.Kind != apperr.KindNotFound || ae.Code != "gitlab_connection_not_found" {
			t.Errorf("TestConnection() error = %v, want NotFound gitlab_connection_not_found", err)
		}
	})
	t.Run("another error passes through", func(t *testing.T) {
		boom := errors.New("secrets file unwritable")
		if _, err := (settingsGitLab{svc: fakeTester{err: boom}}).TestConnection(t.Context(), "x", ""); !errors.Is(err, boom) {
			t.Errorf("TestConnection() error = %v, want %v", err, boom)
		}
	})
}

func TestExtraRedactions_CombinesCustomAndGitLabValues(t *testing.T) {
	sec := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), func(string) string { return "" })
	redactions := extraRedactions(sec)
	if got := redactions(); len(got) != 0 {
		t.Fatalf("extraRedactions() on an empty store = %v, want none", got)
	}
	if _, err := sec.SetCustom("ws", "custom:gw", secrets.CustomSecret{Key: "sk-custom-key-0123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sec.SetGitLab("conn-1", "glpat-0123456789abcdefghij"); err != nil {
		t.Fatal(err)
	}
	got := redactions()
	slices.Sort(got)
	want := []string{"glpat-0123456789abcdefghij", "sk-custom-key-0123456789"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("extraRedactions() mismatch (-want +got):\n%s", diff)
	}
	// Read at each call: a rotation applies at once.
	if _, err := sec.SetGitLab("conn-1", "glpat-rotated0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if got := redactions(); !slices.Contains(got, "glpat-rotated0123456789ab") || slices.Contains(got, "glpat-0123456789abcdefghij") {
		t.Errorf("extraRedactions() after a rotation = %v, want the new token only", got)
	}
}

func TestNewGitLabHTTP_NoProxyNoRedirectGuarded(t *testing.T) {
	// A proxy in the environment must not be used (SCR2).
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	hc := newGitLabHTTP(nil)
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", hc.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Transport.Proxy is set, want none (SCR2)")
	}
	if err := hc.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect() = %v, want http.ErrUseLastResponse", err)
	}
	for _, addr := range []string{"169.254.169.254", "0.0.0.0"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+":81/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		if !egress.IsForbidden(err) {
			t.Errorf("GET %s error = %v, want the forbidden-address refusal", addr, err)
		}
	}
}

// testCA writes a self-signed CA certificate as PEM to path and returns the PEM.
func testCA(t *testing.T, path string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "devdigest test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return pemBytes
}

// mapEnv is a fake process environment; noEnv has no variable set.
type mapEnv map[string]string

func (m mapEnv) get(k string) string { return m[k] }

func noEnv(string) string { return "" }

func TestPrepareGitCA(t *testing.T) {
	t.Run("no extra CA writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		got, err := prepareGitCA(config.Config{DataDir: dir}, noEnv)
		if err != nil || got != "" {
			t.Errorf("prepareGitCA() = %q, %v, want no path", got, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("prepareGitCA() left %d files in the data dir, want none", len(entries))
		}
		if gitCAFile("") != nil {
			t.Error("gitCAFile(\"\") is not a true nil")
		}
	})
	t.Run("the bundle holds the system roots and the extra CA", func(t *testing.T) {
		dir := t.TempDir()
		// A system bundle of our own, so the test does not depend on the host's.
		system := testCA(t, filepath.Join(dir, "system.pem"))
		env := mapEnv{"SSL_CERT_FILE": filepath.Join(dir, "system.pem")}
		extra := testCA(t, filepath.Join(dir, "extra.pem"))
		data := filepath.Join(dir, "data")
		got, err := prepareGitCA(config.Config{DataDir: data, ExtraCAFile: filepath.Join(dir, "extra.pem")}, env.get)
		if err != nil {
			t.Fatalf("prepareGitCA() error = %v", err)
		}
		if want := filepath.Join(data, "git-ca-bundle.pem"); got != want {
			t.Errorf("prepareGitCA() = %q, want %q", got, want)
		}
		bundle, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(bundle) {
			t.Fatal("the bundle holds no certificate")
		}
		for name, pemBytes := range map[string][]byte{"system": system, "extra": extra} {
			block, _ := pem.Decode(pemBytes)
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
				t.Errorf("the %s CA does not verify against the bundle: %v", name, err)
			}
		}
		if f := gitCAFile(got); f == nil || f() != got {
			t.Errorf("gitCAFile(%q) does not return the path", got)
		}
	})
	t.Run("a missing extra file stops the start", func(t *testing.T) {
		dir := t.TempDir()
		missing := filepath.Join(dir, "nope.pem")
		_, err := prepareGitCA(config.Config{DataDir: dir, ExtraCAFile: missing}, noEnv)
		if err == nil {
			t.Fatal("prepareGitCA() = nil error, want one")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("prepareGitCA() error = %v, want it to wrap not-exist", err)
		}
	})
	t.Run("an extra file without a certificate stops the start", func(t *testing.T) {
		dir := t.TempDir()
		bad := filepath.Join(dir, "bad.pem")
		if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := mapEnv{"SSL_CERT_FILE": filepath.Join(dir, "system.pem")}
		testCA(t, filepath.Join(dir, "system.pem"))
		if _, err := prepareGitCA(config.Config{DataDir: dir, ExtraCAFile: bad}, env.get); err == nil {
			t.Error("prepareGitCA() = nil error, want one")
		}
	})
	t.Run("no system bundle refuses to start (SCR5)", func(t *testing.T) {
		dir := t.TempDir()
		testCA(t, filepath.Join(dir, "extra.pem"))
		noBundle := func([]byte, string) error { return gitcli.ErrNoSystemBundle }
		got, err := prepareGitCAWith(config.Config{DataDir: dir, ExtraCAFile: filepath.Join(dir, "extra.pem")}, noBundle)
		if !errors.Is(err, gitcli.ErrNoSystemBundle) || got != "" {
			t.Errorf("prepareGitCA() = %q, %v, want no path and ErrNoSystemBundle", got, err)
		}
	})
}

// fakePaths lists fixed poll targets.
type fakePaths struct {
	targets []pullsdomain.PollTarget
	err     error
}

func (f fakePaths) ListPollTargets(context.Context) ([]pullsdomain.PollTarget, error) {
	return f.targets, f.err
}

func TestIntelGit_RemoteOfAGitLabCloneKey(t *testing.T) {
	repo := func(id string, host string, project int64, owner, name string) pullsdomain.PollTarget {
		return pullsdomain.PollTarget{WorkspaceID: "ws", Repo: pullsdomain.Repo{
			ID: id, Owner: owner, Name: name, Provider: pullsdomain.ProviderGitLab, Host: host, ProjectID: project,
		}}
	}
	gh := pullsdomain.PollTarget{WorkspaceID: "ws", Repo: pullsdomain.Repo{ID: "g", Owner: "acme", Name: "web"}}
	conns := fakeConns{all: []gitlabconnapp.Conn{
		{ID: "1", Host: "gitlab.example:8443", BaseURL: "https://gitlab.example:8443/sub", Token: "glpat-xxxxxxxxxxxxxxxxxxxx"},
	}}
	paths := fakePaths{targets: []pullsdomain.PollTarget{
		gh,
		repo("r0", "gitlab.example:8443", 6, "ops", "other"),
		repo("r1", "gitlab.example:8443", 7, "acme/platform", "web"),
		repo("r2", "gone.example", 9, "acme", "lost"),
		repo("r3", "gitlab.example:8443", 11, "acme/..", "evil"),
	}}
	g := intelGit{conns: conns, paths: paths, caFile: func() string { return "/data/git-ca-bundle.pem" }}
	first, second := codehost.CloneKey(codehost.GitLab, "gitlab.example:8443", 7, "", "")

	t.Run("the remote comes from the connection and the stored path", func(t *testing.T) {
		got, err := g.remote(t.Context(), first, second)
		if err != nil {
			t.Fatalf("remote() error = %v", err)
		}
		want := gitcli.Remote{
			URL:    "https://gitlab.example:8443/sub/acme/platform/web.git",
			Prefix: "https://gitlab.example:8443/sub/",
			Token:  "glpat-xxxxxxxxxxxxxxxxxxxx",
			CAFile: "/data/git-ca-bundle.pem",
		}
		if got != want {
			t.Errorf("remote() = %v, want %v", got, want)
		}
	})
	t.Run("a host without a connection is not connected", func(t *testing.T) {
		f, s := codehost.CloneKey(codehost.GitLab, "gone.example", 9, "", "")
		_, err := g.remote(t.Context(), f, s)
		var nc *reposapp.NotConnectedError
		if !errors.As(err, &nc) || nc.Host != "gone.example" {
			t.Errorf("remote() error = %v, want *NotConnectedError for gone.example", err)
		}
		// The public entry points stop there: no git command runs.
		if _, err := g.DefaultBranch(t.Context(), f, s); !errors.As(err, &nc) {
			t.Errorf("DefaultBranch() error = %v, want *NotConnectedError", err)
		}
		if _, err := g.Sync(t.Context(), f, s, "main"); !errors.As(err, &nc) {
			t.Errorf("Sync() error = %v, want *NotConnectedError", err)
		}
	})
	t.Run("an unknown project has no remote", func(t *testing.T) {
		f, s := codehost.CloneKey(codehost.GitLab, "gitlab.example:8443", 99, "", "")
		if _, err := g.remote(t.Context(), f, s); err == nil {
			t.Error("remote() = nil error for an unknown project, want one")
		}
	})
	t.Run("a project id of another host does not match", func(t *testing.T) {
		f, s := codehost.CloneKey(codehost.GitLab, "other.example", 7, "", "")
		if _, err := g.remote(t.Context(), f, s); err == nil {
			t.Error("remote() = nil error for another host, want one")
		}
	})
	t.Run("a stored path that is not plain is refused", func(t *testing.T) {
		f, s := codehost.CloneKey(codehost.GitLab, "gitlab.example:8443", 11, "", "")
		if _, err := g.remote(t.Context(), f, s); err == nil {
			t.Error("remote() = nil error for a path with .., want one")
		}
	})
	t.Run("a malformed project id", func(t *testing.T) {
		if _, err := g.remote(t.Context(), first, "7x"); err == nil {
			t.Error("remote() = nil error for a non-numeric id, want one")
		}
	})
	t.Run("the store failing is passed on", func(t *testing.T) {
		boom := errors.New("db down")
		_, err := intelGit{conns: conns, paths: fakePaths{err: boom}}.remote(t.Context(), first, second)
		if !errors.Is(err, boom) {
			t.Errorf("remote() error = %v, want %v", err, boom)
		}
	})
}

func TestIsGitLabKey(t *testing.T) {
	for in, want := range map[string]bool{
		"@gitlab/gitlab.com": true, "@gitlab/__1-_8443": true, "acme": false, "@gitlab": false, "": false,
	} {
		if got := isGitLabKey(in); got != want {
			t.Errorf("isGitLabKey(%q) = %v, want %v", in, got, want)
		}
	}
}

// ---- GitLab B: the composition root binds the connections to comments and the context peers ----

// Component tests of the GitLab B wiring (plan S19, [C5]) through the real server: every service
// that reads or writes GitLab for a repository got its GitLab dependency, resolves the connection
// at every call and sends its requests to the repository's own connection with that connection's
// token, and no GitLab repository causes a GitHub request. The harness is the GitLab A isolation
// stub (giBoot); helpers here start with gw.

const (
	gwToken2     = "glpat-RotatedTokenValue01"
	gwDiscussion = "abcdef0123456789abcdef0123456789abcdef01"
	gwNoteID     = 901
	gwDoc        = "docs/design.md"
	gwDocText    = "# Design\n\nThe limiter is a token bucket.\n"
)

// gwDiscussionJSON is a diff thread on a.ts line 3 by ann, at the stub's head sha.
func gwDiscussionJSON() map[string]any {
	return map[string]any{"id": gwDiscussion, "notes": []any{map[string]any{
		"id": gwNoteID, "type": "DiffNote", "system": false, "body": "Looks wrong", "author": map[string]any{"username": "ann"},
		"created_at": "2026-10-20T10:00:00Z",
		"position": map[string]any{"position_type": "text", "base_sha": "b0", "start_sha": "s0", "head_sha": giHeadSHA,
			"old_path": "a.ts", "new_path": "a.ts", "old_line": nil, "new_line": 3},
	}}}
}

// gwGitLab answers what GitLab B reads and writes on top of the stub's defaults: the discussions
// of !3, the reply note (or, while rejectNote is set, a 403 that repeats the token like a careless
// proxy), the doc and the skill file.
type gwGitLab struct {
	rejectNote atomic.Bool
}

func (g *gwGitLab) handle(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/projects/7/merge_requests/3/discussions"):
		giJSON(w, http.StatusOK, []any{gwDiscussionJSON()})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/discussions/"+gwDiscussion+"/notes"):
		if g.rejectNote.Load() {
			giJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden: token " + r.Header.Get("PRIVATE-TOKEN") + " cannot comment"})
			return true
		}
		var in struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		giJSON(w, http.StatusCreated, map[string]any{"id": 902, "type": nil, "system": false, "body": in.Body,
			"author": map[string]any{"username": "ada"}, "created_at": "2026-10-21T10:00:00Z"})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/projects/7/merge_requests/3"):
		// The detail answer names a doc of the project in the description (the list answer has none).
		m := map[string]any{"iid": 3, "title": "Add feature", "state": "opened", "draft": false, "source_branch": "feat",
			"target_branch": "main", "sha": giHeadSHA, "source_project_id": giProjectID, "target_project_id": giProjectID,
			"created_at": "2026-10-19T10:00:00Z", "updated_at": "2026-10-20T10:00:00Z", "merged_at": nil, "closed_at": nil,
			"author":      map[string]any{"username": "octo"},
			"description": "Fixes #12. Design: " + gwDoc,
		}
		giJSON(w, http.StatusOK, m)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/repository/files/"+gwDoc+"/raw"):
		_, _ = io.WriteString(w, gwDocText)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/repository/files/skills/limiter.md/raw"):
		_, _ = io.WriteString(w, "---\nname: limiter\ndescription: Rate limits\n---\nUse a token bucket.\n")
	default:
		return false
	}
	return true
}

// gwRequests are the GitLab requests after the first n whose "METHOD path" contains sub.
func gwRequests(e *giEnv, n int, sub string) []giReq {
	var out []giReq
	for _, r := range e.gl.since(n) {
		if strings.Contains(r.Method+" "+r.Path, sub) {
			out = append(out, r)
		}
	}
	return out
}

// gwBoot starts the stack with a connection, the acme/web repository and its merge request !3.
func gwBoot(t *testing.T) (e *giEnv, gl *gwGitLab, connID, pr string) {
	t.Helper()
	e = giBoot(t, giOpts{})
	connID = e.connect(giToken)
	repo := e.addRepo(giPath)
	pr = e.pullID(repo)
	gl = &gwGitLab{}
	e.gl.setHook(gl.handle)
	// The detail read stores the description (the intent and the brief read it from the row).
	if resp := e.s.get("/pulls/" + pr); resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	return e, gl, connID, pr
}

// GitLab B P9: the pulls service holds the comments port and the redactor. A read lists the
// thread, a reply is posted, both with the connection's token and none to GitHub; a rejected
// write that repeats the token reaches neither the response nor the log; a rotated token applies
// to the next request and a removed one stops it before any request.
func TestGitLabBWiring_CommentsUseTheConnectionAtEveryCall(t *testing.T) {
	e, gl, connID, pr := gwBoot(t)
	_, ghBefore := e.lookups()
	n := e.gl.count()

	resp := e.s.get("/pulls/" + pr + "/comments")
	got := list(t, resp)
	if resp.Status != http.StatusOK || len(got) != 1 || got[0]["id"] != float64(gwNoteID) || got[0]["body"] != "Looks wrong" || got[0]["path"] != "a.ts" {
		t.Fatalf("GET /pulls/:id/comments = %d %s, want the one thread of the stub", resp.Status, resp.Body)
	}
	if reqs := gwRequests(e, n, "GET /api/v4/projects/7/merge_requests/3/discussions"); len(reqs) != 1 || reqs[0].Private != giToken {
		t.Errorf("the read sent %+v, want one discussions request with the connection's token", reqs)
	}

	n = e.gl.count()
	reply := map[string]any{"path": "a.ts", "line": 3, "body": "Thanks", "in_reply_to": gwNoteID}
	resp = e.s.post("/pulls/"+pr+"/comments", reply)
	if resp.Status != http.StatusOK || resp.Obj(t)["body"] != "Thanks" || resp.Obj(t)["in_reply_to_id"] != float64(gwNoteID) {
		t.Fatalf("POST reply = %d %s, want 200 with the posted note in reply to %d", resp.Status, resp.Body, gwNoteID)
	}
	posts := gwRequests(e, n, "POST ")
	if len(posts) != 1 || !strings.HasSuffix(posts[0].Path, "/discussions/"+gwDiscussion+"/notes") || posts[0].Private != giToken ||
		!strings.Contains(string(posts[0].Body), `"body":"Thanks"`) {
		t.Errorf("the reply sent %+v, want one note POST with the token and the body", posts)
	}

	t.Run("a rejected write is redacted", func(t *testing.T) {
		gl.rejectNote.Store(true)
		defer gl.rejectNote.Store(false)
		resp := e.s.post("/pulls/"+pr+"/comments", reply)
		if resp.Status != http.StatusBadRequest || resp.errorCode(t) != "gitlab_permission_denied" {
			t.Fatalf("POST rejected reply = %d %s, want 400 gitlab_permission_denied", resp.Status, resp.Body)
		}
		assertNoSecret(t, "the response", string(resp.Body)+fmt.Sprint(resp.Header), giToken)
		assertNoSecret(t, "the log", e.s.logs.String(), giToken)
		if !strings.Contains(string(resp.Body), "[REDACTED]") {
			t.Errorf("the response %s holds no [REDACTED]; GitLab's text was not passed through the redactor", resp.Body)
		}
	})
	t.Run("a rotated token applies to the next request", func(t *testing.T) {
		if r := e.s.do(http.MethodPut, "/settings/gitlab-connections/"+connID, map[string]any{"token": gwToken2}); r.Status != http.StatusOK {
			t.Fatalf("PUT token = %d %s, want 200", r.Status, r.Body)
		}
		n := e.gl.count()
		if r := e.s.get("/pulls/" + pr + "/comments"); r.Status != http.StatusOK || len(list(t, r)) != 1 {
			t.Fatalf("GET comments after the rotation = %d %s, want 200 with the thread", r.Status, r.Body)
		}
		if reqs := gwRequests(e, n, "/discussions"); len(reqs) != 1 || reqs[0].Private != gwToken2 {
			t.Errorf("after the rotation the read sent %+v, want the new token", reqs)
		}
	})
	t.Run("a removed token sends no request", func(t *testing.T) {
		if r := e.s.do(http.MethodPut, "/settings/gitlab-connections/"+connID, map[string]any{"token": nil}); r.Status != http.StatusOK {
			t.Fatalf("PUT token null = %d %s, want 200", r.Status, r.Body)
		}
		n := e.gl.count()
		if r := e.s.get("/pulls/" + pr + "/comments"); r.Status != http.StatusOK || strings.TrimSpace(string(r.Body)) != "[]" {
			t.Errorf("GET comments without a token = %d %s, want 200 []", r.Status, r.Body)
		}
		if r := e.s.post("/pulls/"+pr+"/comments", reply); r.Status != http.StatusUnprocessableEntity && r.Status != http.StatusBadRequest {
			t.Errorf("POST reply without a token = %d %s, want a 4xx refusal", r.Status, r.Body)
		}
		if reqs := e.gl.since(n); len(reqs) != 0 {
			t.Errorf("a repository without a token sent %d request(s), want none: %+v", len(reqs), reqs)
		}
	})

	if _, gh := e.lookups(); gh != ghBefore {
		t.Errorf("the comment flows sent %d GitHub request(s), want none: %v", gh-ghBefore, e.s.gh.requests()[ghBefore:])
	}
}

// GitLab B P9: blast history, the brief's issue, the intent's ticket and doc, and the skill file
// import each got their GitLab dependency: every one sends its request to the connection with
// its token, and GitHub sees nothing.
func TestGitLabBWiring_ContextPeersReadThroughTheConnection(t *testing.T) {
	e, _, _, pr := gwBoot(t)
	_, ghBefore := e.lookups()
	n := e.gl.count()

	t.Run("blast history", func(t *testing.T) {
		before := e.gl.count()
		if r := e.s.get("/pulls/" + pr + "/history"); r.Status != http.StatusOK {
			t.Fatalf("GET history = %d %s, want 200", r.Status, r.Body)
		}
		reqs := gwRequests(e, before, "GET /api/v4/projects/7/merge_requests")
		if len(reqs) == 0 || !strings.Contains(reqs[0].Query, "state=merged") || reqs[0].Private != giToken {
			t.Errorf("history sent %+v, want the merged list with the token", reqs)
		}
	})
	t.Run("intent ticket and doc", func(t *testing.T) {
		before := e.gl.count()
		resp := e.s.post("/pulls/"+pr+"/intent/refresh", nil)
		if resp.Status != http.StatusOK {
			t.Fatalf("POST intent/refresh = %d %s, want 200", resp.Status, resp.Body)
		}
		if reqs := gwRequests(e, before, "GET /api/v4/projects/7/issues/12"); len(reqs) != 1 || reqs[0].Private != giToken {
			t.Errorf("the intent sent %+v for the ticket, want one issue read with the token", reqs)
		}
		reqs := gwRequests(e, before, "/repository/files/"+gwDoc+"/raw")
		if len(reqs) != 1 || reqs[0].Private != giToken || !strings.Contains(reqs[0].Query, "ref="+giHeadSHA) {
			t.Errorf("the intent sent %+v for the doc, want one file read at the head sha with the token", reqs)
		}
		used := map[string]string{}
		for _, src := range obj(t, resp.JSON(t), "intent")["sources"].([]any) {
			m := src.(map[string]any)
			used[fmt.Sprint(m["kind"], ":", m["ref"])] = fmt.Sprint(m["status"])
		}
		if used["ticket:12"] != "used" {
			t.Errorf("intent sources = %v, want ticket 12 used", used)
		}
	})
	t.Run("brief issue", func(t *testing.T) {
		before := e.gl.count()
		if r := e.s.post("/pulls/"+pr+"/brief", nil); r.Status != http.StatusOK {
			t.Fatalf("POST brief = %d %s, want 200", r.Status, r.Body)
		}
		if reqs := gwRequests(e, before, "GET /api/v4/projects/7/issues/12"); len(reqs) != 1 || reqs[0].Private != giToken {
			t.Errorf("the brief sent %+v for the issue, want one read with the token", reqs)
		}
	})
	t.Run("skill file import", func(t *testing.T) {
		before := e.gl.count()
		url := e.gl.baseURL() + "/" + giPath + "/-/blob/main/skills/limiter.md"
		resp := e.s.post("/skills/import/preview", map[string]any{"kind": "url", "url": url})
		if resp.Status != http.StatusOK || !strings.Contains(fmt.Sprint(resp.Obj(t)["body"]), "token bucket") {
			t.Fatalf("POST import preview of %s = %d %s, want 200 with the file's body", url, resp.Status, resp.Body)
		}
		reqs := gwRequests(e, before, "/repository/files/")
		if len(reqs) == 0 || reqs[0].Private != giToken || !strings.Contains(reqs[0].Query, "ref=main") {
			t.Errorf("the import sent %+v, want the raw file read at ref main with the token", reqs)
		}
	})

	if e.gl.count() == n {
		t.Error("the stub saw no request; the wiring proves nothing")
	}
	if _, gh := e.lookups(); gh != ghBefore {
		t.Errorf("the GitLab reads sent %d GitHub request(s), want none: %v", gh-ghBefore, e.s.gh.requests()[ghBefore:])
	}
}
