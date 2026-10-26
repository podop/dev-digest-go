package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

func TestPrepareGitCA(t *testing.T) {
	t.Run("no extra CA writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		got, err := prepareGitCA(config.Config{DataDir: dir})
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
		t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "system.pem"))
		extra := testCA(t, filepath.Join(dir, "extra.pem"))
		data := filepath.Join(dir, "data")
		got, err := prepareGitCA(config.Config{DataDir: data, ExtraCAFile: filepath.Join(dir, "extra.pem")})
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
		_, err := prepareGitCA(config.Config{DataDir: dir, ExtraCAFile: missing})
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
		t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "system.pem"))
		testCA(t, filepath.Join(dir, "system.pem"))
		if _, err := prepareGitCA(config.Config{DataDir: dir, ExtraCAFile: bad}); err == nil {
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
