package gitcli_test

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/gitcli"
)

const (
	glToken    = "glpat-SECRET-token-1"
	glHost     = "example.com" // the name the httptest TLS certificate is valid for
	glOwnerKey = "@gitlab/example.com_443"
)

// gitlabStub is a TLS smart-HTTP git server (git-http-backend) that records what it was sent.
type gitlabStub struct {
	srv    *httptest.Server
	caFile string
	prefix string // https://example.com:<port>/
	url    string // clone URL of the repository
	remote *remote

	mu    sync.Mutex
	auths []string
	paths []string
}

// newGitLabStub serves r's bare repository over TLS. With wantAuth set, a request without that
// Authorization header gets 401 and never reaches git.
func newGitLabStub(t *testing.T, r *remote, wantAuth string) *gitlabStub {
	t.Helper()
	backend := filepath.Join(strings.TrimSpace(git(t, ".", "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	bare := strings.TrimPrefix(r.url, "file://")
	cgiH := &cgi.Handler{
		Path:       backend,
		Root:       "/",
		Env:        []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"PATH", "HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"},
	}
	s := &gitlabStub{remote: r}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, req.Header.Get("Authorization"))
		s.paths = append(s.paths, req.URL.Path)
		s.mu.Unlock()
		if wantAuth != "" && req.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiH.ServeHTTP(w, req)
	}))
	t.Cleanup(s.srv.Close)
	s.caFile = writeCert(t, s.srv.Certificate().Raw)
	s.prefix = fmt.Sprintf("https://%s:%s/", glHost, portOf(s.srv))
	s.url = s.prefix + filepath.Base(bare)
	return s
}

func portOf(s *httptest.Server) string {
	_, port, _ := strings.Cut(strings.TrimPrefix(s.URL, "https://"), ":")
	return port
}

func writeCert(t *testing.T, der []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (s *gitlabStub) requests() (auths, paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...), append([]string(nil), s.paths...)
}

func (s *gitlabStub) remoteWith(token string) gitcli.Remote {
	return gitcli.Remote{URL: s.url, Prefix: s.prefix, Token: token, CAFile: s.caFile}
}

// resolveTo answers every lookup with addrs and records the host names asked for.
func resolveTo(asked *[]string, addrs ...string) func(ctx context.Context, host string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		*asked = append(*asked, host)
		out := make([]netip.Addr, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

// netWrapper logs the network-related environment git was started with, then runs git.
func netWrapper(t *testing.T) (bin, argvLog, envLog string) {
	t.Helper()
	envLog = filepath.Join(t.TempDir(), "env.log")
	bin, argvLog = wrapper(t, fmt.Sprintf(
		`env | grep -i -E '^(https?_proxy|all_proxy|ftp_proxy|no_proxy|git_ssl_[a-z_]*|curl_ca_bundle)=' >> %q; exec git "$@"`, envLog))
	return bin, argvLog, envLog
}

// testCtx bounds a real-git test so a regression fails instead of hanging.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRemote_CloneSyncDefaultBranchOverTLS(t *testing.T) {
	hermeticGit(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1") // a dead proxy: any use fails the run
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	r := newRemote(t)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+glToken))
	stub := newGitLabStub(t, r, wantAuth)
	bin, argvLog, envLog := netWrapper(t)
	var asked []string
	root := t.TempDir()
	c := newClient(t, gitcli.Options{
		Binary: bin, CloneRoot: root, Token: tokenOf("ghp_must_not_be_sent"),
		Resolver: resolveTo(&asked, "127.0.0.1"),
	})
	rem := stub.remoteWith(glToken)
	ctx := testCtx(t)

	dest, err := c.CloneRemote(ctx, glOwnerKey, "42", rem, gitcli.CloneOptions{Depth: 1})
	if err != nil {
		t.Fatalf("CloneRemote() error: %v", err)
	}
	if want := filepath.Join(root, "@gitlab", "example.com_443", "42"); dest != want {
		t.Errorf("CloneRemote() = %q, want %q", dest, want)
	}
	if b := readFile(t, filepath.Join(dest, "a.txt")); b != "one" {
		t.Errorf("a.txt = %q, want %q", b, "one")
	}
	sha := r.commit("b.txt", "two")
	got, err := c.SyncRemote(ctx, glOwnerKey, "42", "main", rem)
	if err != nil || got != sha {
		t.Fatalf("SyncRemote() = %q, %v, want %q", got, err, sha)
	}
	branch, err := c.DefaultBranchRemote(ctx, glOwnerKey, "42", rem)
	if err != nil || branch != "main" {
		t.Fatalf("DefaultBranchRemote() = %q, %v, want main", branch, err)
	}
	// A second CloneRemote on the existing clone fetches.
	if _, err := c.CloneRemote(ctx, glOwnerKey, "42", rem, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("CloneRemote() on an existing clone error: %v", err)
	}

	auths, _ := stub.requests()
	if len(auths) == 0 {
		t.Fatal("the server saw no request")
	}
	for _, a := range auths {
		if a != wantAuth {
			t.Errorf("request Authorization = %q, want %q", a, wantAuth)
		}
	}

	calls := readFile(t, argvLog)
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "ARGV:") && (strings.Contains(line, glToken) || strings.Contains(line, "Authorization") ||
			strings.Contains(line, "ghp_must_not_be_sent")) {
			t.Errorf("argv leaks credentials: %s", line)
		}
	}
	for _, want := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + stub.prefix + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: " + wantAuth,
		"[http.followRedirects=false]",
		"[http.proxy=]",
		"[http.sslVerify=true]",
		"[http.sslCAInfo=" + stub.caFile + "]",
		"[http.curloptResolve=" + glHost + ":" + portOf(stub.srv) + ":127.0.0.1]",
		"[core.hooksPath=/dev/null]",
		"GIT_TERMINAL_PROMPT=0",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("git invocations lack %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "x-access-token") || strings.Contains(calls, base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_must_not_be_sent"))) {
		t.Errorf("the GitHub token reached a GitLab remote:\n%s", calls)
	}
	if env := readFile(t, envLog); strings.TrimSpace(env) != "" {
		t.Errorf("proxy or TLS override variables reached git:\n%s", env)
	}
	if len(asked) != 4 {
		t.Errorf("host lookups = %v, want one per operation (4)", asked)
	}

	cfg := readFile(t, filepath.Join(dest, ".git", "config"))
	remotes := git(t, dest, "remote", "-v")
	for name, body := range map[string]string{".git/config": cfg, "git remote -v": remotes} {
		if strings.Contains(body, glToken) || strings.Contains(body, "Basic") || strings.Contains(body, "Authorization") {
			t.Errorf("%s contains credentials:\n%s", name, body)
		}
		if !strings.Contains(body, stub.url) {
			t.Errorf("%s lacks the origin URL %s:\n%s", name, stub.url, body)
		}
	}
}

func TestRemote_RenamedProjectMovesOrigin(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	stub := newGitLabStub(t, r, "")
	var asked []string
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir(), Resolver: resolveTo(&asked, "127.0.0.1")})
	rem := stub.remoteWith("")
	dest, err := c.CloneRemote(testCtx(t), glOwnerKey, "7", rem, gitcli.CloneOptions{})
	if err != nil {
		t.Fatalf("CloneRemote() error: %v", err)
	}
	// The project moved: the old path no longer exists, only the new one does.
	git(t, dest, "remote", "set-url", "origin", stub.prefix+"gone.git")
	if _, err := c.SyncRemote(testCtx(t), glOwnerKey, "7", "main", rem); err != nil {
		t.Fatalf("SyncRemote() after the remote moved error: %v", err)
	}
	if got := git(t, dest, "remote", "get-url", "origin"); got != stub.url {
		t.Errorf("origin = %q, want %q", got, stub.url)
	}
}

func TestRemote_AnonymousSendsNoAuthorization(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	stub := newGitLabStub(t, r, "")
	bin, argvLog, _ := netWrapper(t)
	var asked []string
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir(), Token: tokenOf("ghp_x"), Resolver: resolveTo(&asked, "127.0.0.1")})
	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "9", stub.remoteWith(""), gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("CloneRemote() error: %v", err)
	}
	auths, _ := stub.requests()
	for _, a := range auths {
		if a != "" {
			t.Errorf("anonymous request carried Authorization %q", a)
		}
	}
	for _, bad := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY", "GIT_CONFIG_VALUE", "Authorization:"} {
		if strings.Contains(readFile(t, argvLog), bad) {
			t.Errorf("anonymous run contains %q", bad)
		}
	}
}

func TestRemote_RedirectIsNotFollowed(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	other := newGitLabStub(t, r, "")
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.prefix+strings.TrimPrefix(req.URL.Path, "/"), http.StatusMovedPermanently)
	}))
	t.Cleanup(redirector.Close)
	prefix := fmt.Sprintf("https://%s:%s/", glHost, portOf(redirector))
	rem := gitcli.Remote{URL: prefix + "origin.git", Prefix: prefix, Token: glToken, CAFile: other.caFile}
	var asked []string
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir(), Resolver: resolveTo(&asked, "127.0.0.1")})

	_, err := c.CloneRemote(testCtx(t), glOwnerKey, "5", rem, gitcli.CloneOptions{Depth: 1})
	if err == nil {
		t.Fatal("CloneRemote() through a redirect = nil error, want failure")
	}
	if auths, _ := other.requests(); len(auths) != 0 {
		t.Errorf("redirect target was contacted %d times (auth %q)", len(auths), auths)
	}
	if strings.Contains(err.Error(), glToken) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestRemote_CAFileIsTheTrustStore(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	stub := newGitLabStub(t, r, "")
	var asked []string
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir(), Resolver: resolveTo(&asked, "127.0.0.1")})
	rem := stub.remoteWith("")
	rem.CAFile = "" // git falls back to the system store, which does not trust the stub

	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "6", rem, gitcli.CloneOptions{Depth: 1}); err == nil {
		t.Fatal("CloneRemote() without the CA = nil error, want a certificate failure")
	}
	if auths, _ := stub.requests(); len(auths) != 0 {
		t.Errorf("an untrusted server received %d requests", len(auths))
	}
	rem.CAFile = stub.caFile
	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "6", rem, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("CloneRemote() with the CA error: %v", err)
	}
}

func TestRemote_ForbiddenAddressIsRefusedBeforeGitRuns(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	stub := newGitLabStub(t, r, "")
	tests := []struct {
		name  string
		addrs []string
		url   string
	}{
		{name: "link-local metadata", addrs: []string{"169.254.169.254"}},
		{name: "one bad among good", addrs: []string{"127.0.0.1", "169.254.169.254"}},
		{name: "mapped link-local", addrs: []string{"::ffff:169.254.169.254"}},
		{name: "unspecified", addrs: []string{"0.0.0.0"}},
		{name: "ipv6 link-local", addrs: []string{"fe80::1"}},
		{name: "ip literal", url: "https://169.254.169.254/g/p.git"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin, argvLog, _ := netWrapper(t)
			root := t.TempDir()
			var asked []string
			c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: root, Resolver: resolveTo(&asked, tc.addrs...)})
			rem := stub.remoteWith(glToken)
			if tc.url != "" {
				rem.URL, rem.Prefix = tc.url, "https://169.254.169.254/"
			}
			if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", rem, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrForbiddenAddress) {
				t.Errorf("CloneRemote() error = %v, want ErrForbiddenAddress", err)
			}
			if _, err := os.Stat(argvLog); err == nil {
				t.Errorf("git ran:\n%s", readFile(t, argvLog))
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Errorf("clone root is not empty after a refused remote: %v", entries)
			}
			if auths, _ := stub.requests(); len(auths) != 0 {
				t.Errorf("server contacted %d times", len(auths))
			}
		})
	}
}

func TestRemote_ForbiddenAddressOnExistingClone(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	stub := newGitLabStub(t, r, "")
	var asked []string
	root := t.TempDir()
	c := newClient(t, gitcli.Options{CloneRoot: root, Resolver: resolveTo(&asked, "127.0.0.1")})
	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "3", stub.remoteWith(""), gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("CloneRemote() error: %v", err)
	}
	// DNS now answers with the metadata address (rebinding): the refresh is refused.
	rebound := newClient(t, gitcli.Options{CloneRoot: root, Resolver: resolveTo(&asked, "169.254.169.254")})
	if _, err := rebound.SyncRemote(testCtx(t), glOwnerKey, "3", "main", stub.remoteWith("")); !errors.Is(err, gitcli.ErrForbiddenAddress) {
		t.Errorf("SyncRemote() error = %v, want ErrForbiddenAddress", err)
	}
	if _, err := rebound.DefaultBranchRemote(testCtx(t), glOwnerKey, "3", stub.remoteWith("")); !errors.Is(err, gitcli.ErrForbiddenAddress) {
		t.Errorf("DefaultBranchRemote() error = %v, want ErrForbiddenAddress", err)
	}
}

func TestRemote_ResolverFailureAndEmptyAnswer(t *testing.T) {
	hermeticGit(t)
	root := t.TempDir()
	rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/"}
	boom := errors.New("no such host")
	c := newClient(t, gitcli.Options{CloneRoot: root, Resolver: func(context.Context, string) ([]netip.Addr, error) { return nil, boom }})
	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", rem, gitcli.CloneOptions{}); !errors.Is(err, boom) {
		t.Errorf("CloneRemote() error = %v, want the resolver error", err)
	}
	c = newClient(t, gitcli.Options{CloneRoot: root, Resolver: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }})
	if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", rem, gitcli.CloneOptions{}); err == nil {
		t.Error("CloneRemote() with no address = nil error, want failure")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("clone root is not empty: %v", entries)
	}
}

func TestRemote_InvalidRemoteNeverReachesGit(t *testing.T) {
	hermeticGit(t)
	const p = "https://gitlab.example.org/"
	tests := []struct {
		name string
		r    gitcli.Remote
	}{
		{name: "empty", r: gitcli.Remote{}},
		{name: "http scheme", r: gitcli.Remote{URL: "http://gitlab.example.org/g/p.git", Prefix: "http://gitlab.example.org/"}},
		{name: "file scheme", r: gitcli.Remote{URL: "file:///etc", Prefix: "file:///"}},
		{name: "ssh scheme", r: gitcli.Remote{URL: "ssh://git@gitlab.example.org/g/p.git", Prefix: p}},
		{name: "userinfo", r: gitcli.Remote{URL: "https://u:pw@gitlab.example.org/g/p.git", Prefix: p}},
		{name: "query", r: gitcli.Remote{URL: p + "g/p.git?x=1", Prefix: p}},
		{name: "fragment", r: gitcli.Remote{URL: p + "g/p.git#x", Prefix: p}},
		{name: "prefix of another host", r: gitcli.Remote{URL: p + "g/p.git", Prefix: "https://evil.example.org/"}},
		{name: "prefix with other port", r: gitcli.Remote{URL: p + "g/p.git", Prefix: "https://gitlab.example.org:8443/"}},
		{name: "prefix not a prefix", r: gitcli.Remote{URL: p + "g/p.git", Prefix: p + "x/"}},
		{name: "prefix without slash", r: gitcli.Remote{URL: p + "g/p.git", Prefix: "https://gitlab.example.org"}},
		{name: "prefix wildcard", r: gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://*.example.org/"}},
		{name: "prefix everything", r: gitcli.Remote{URL: p + "g/p.git", Prefix: "https://"}},
		{name: "space in url", r: gitcli.Remote{URL: p + "g/p .git", Prefix: p}},
		{name: "newline in prefix", r: gitcli.Remote{URL: p + "g/p.git", Prefix: p + "\n"}},
		{name: "relative ca file", r: gitcli.Remote{URL: p + "g/p.git", Prefix: p, CAFile: "ca.pem"}},
		{name: "space in ca file", r: gitcli.Remote{URL: p + "g/p.git", Prefix: p, CAFile: "/a b/ca.pem"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin, argvLog, _ := netWrapper(t)
			root := t.TempDir()
			c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: root, Resolver: func(context.Context, string) ([]netip.Addr, error) {
				t.Error("the host was looked up for an invalid remote")
				return nil, nil
			}})
			if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", tc.r, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrInvalidRemote) {
				t.Errorf("CloneRemote(%v) error = %v, want ErrInvalidRemote", tc.r, err)
			}
			if _, err := os.Stat(argvLog); err == nil {
				t.Errorf("git ran:\n%s", readFile(t, argvLog))
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Errorf("clone root is not empty: %v", entries)
			}
		})
	}
}

func TestRemote_StderrAndErrorsAreScrubbed(t *testing.T) {
	hermeticGit(t)
	basic := base64.StdEncoding.EncodeToString([]byte("oauth2:" + glToken))
	bin, _ := wrapper(t, fmt.Sprintf(
		`echo "fatal: leaked %s and Authorization: Basic %s and https://oauth2:%s@gitlab.example.org/x" >&2; exit 128`, glToken, basic, glToken))
	var asked []string
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir(), Resolver: resolveTo(&asked, "127.0.0.1")})
	rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/", Token: glToken}
	_, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", rem, gitcli.CloneOptions{})
	if err == nil {
		t.Fatal("CloneRemote() = nil error, want the git failure")
	}
	for _, bad := range []string{glToken, basic} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error leaks %q: %v", bad, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("error = %v, want the scrubbed stderr", err)
	}
}

func TestRemote_TokenNeverInFormattedRemote(t *testing.T) {
	rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/", Token: glToken}
	for name, got := range map[string]string{
		"%v": fmt.Sprintf("%v", rem), "%+v": fmt.Sprintf("%+v", rem), "%#v": fmt.Sprintf("%#v", rem), "%s": fmt.Sprintf("%s", rem),
	} {
		if strings.Contains(got, glToken) {
			t.Errorf("Sprintf(%s, Remote) leaks the token: %s", name, got)
		}
	}
}

func TestRemote_KeysAreSeparatedByProvider(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	root := t.TempDir()
	var asked []string
	c := newClient(t, gitcli.Options{CloneRoot: root, Resolver: resolveTo(&asked, "127.0.0.1")})
	rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/"}

	if _, err := c.Clone(testCtx(t), glOwnerKey, "42", r.url, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrGitLabKey) {
		t.Errorf("Clone(GitLab key) error = %v, want ErrGitLabKey", err)
	}
	if _, err := c.Sync(testCtx(t), glOwnerKey, "42", "main"); !errors.Is(err, gitcli.ErrGitLabKey) {
		t.Errorf("Sync(GitLab key) error = %v, want ErrGitLabKey", err)
	}
	if _, err := c.DefaultBranch(testCtx(t), glOwnerKey, "42"); !errors.Is(err, gitcli.ErrGitLabKey) {
		t.Errorf("DefaultBranch(GitLab key) error = %v, want ErrGitLabKey", err)
	}
	if _, err := c.CloneRemote(testCtx(t), "octo", "hello", rem, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrNotGitLabKey) {
		t.Errorf("CloneRemote(GitHub key) error = %v, want ErrNotGitLabKey", err)
	}
	if _, err := c.SyncRemote(testCtx(t), "octo", "hello", "main", rem); !errors.Is(err, gitcli.ErrNotGitLabKey) {
		t.Errorf("SyncRemote(GitHub key) error = %v, want ErrNotGitLabKey", err)
	}
	if _, err := c.DefaultBranchRemote(testCtx(t), "octo", "hello", rem); !errors.Is(err, gitcli.ErrNotGitLabKey) {
		t.Errorf("DefaultBranchRemote(GitHub key) error = %v, want ErrNotGitLabKey", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("clone root is not empty: %v", entries)
	}
}

func TestRemote_SymlinkAtEveryLevelIsRejected(t *testing.T) {
	hermeticGit(t)
	var asked []string
	rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/"}
	tests := []struct {
		name string
		link string // path under the clone root that becomes a symlink to the outside directory
		make []string
	}{
		{name: "top level @gitlab", link: "@gitlab"},
		{name: "host level", link: "@gitlab/example.com_443", make: []string{"@gitlab"}},
		{name: "project level", link: "@gitlab/example.com_443/42", make: []string{"@gitlab/example.com_443"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root, outside := filepath.Join(base, "clones"), filepath.Join(base, "outside")
			victim := filepath.Join(outside, "precious.txt")
			for _, d := range append([]string{root, outside}, prefixed(root, tc.make)...) {
				if err := os.MkdirAll(d, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, filepath.FromSlash(tc.link))); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			c := newClient(t, gitcli.Options{CloneRoot: root, Resolver: resolveTo(&asked, "127.0.0.1")})
			if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "42", rem, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrUnsafePath) {
				t.Errorf("CloneRemote() error = %v, want ErrUnsafePath", err)
			}
			if _, err := c.SyncRemote(testCtx(t), glOwnerKey, "42", "main", rem); !errors.Is(err, gitcli.ErrUnsafePath) {
				t.Errorf("SyncRemote() error = %v, want ErrUnsafePath", err)
			}
			if _, err := c.DefaultBranchRemote(testCtx(t), glOwnerKey, "42", rem); !errors.Is(err, gitcli.ErrUnsafePath) {
				t.Errorf("DefaultBranchRemote() error = %v, want ErrUnsafePath", err)
			}
			if _, err := c.Head(testCtx(t), glOwnerKey, "42"); !errors.Is(err, gitcli.ErrUnsafePath) {
				t.Errorf("Head() error = %v, want ErrUnsafePath", err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 1 || entries[0].Name() != "precious.txt" {
				t.Errorf("the directory behind the symlink changed: %v", entries)
			}
			if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
				t.Errorf("file behind the symlink changed: %q, %v", b, err)
			}
		})
	}
}

func prefixed(root string, rel []string) []string {
	out := make([]string, 0, len(rel))
	for _, r := range rel {
		out = append(out, filepath.Join(root, filepath.FromSlash(r)))
	}
	return out
}

// A GitHub clone of an owner named "gitlab" and a GitLab clone never share a directory (EC22).
func TestRemote_GitHubAndGitLabDirectoriesNeverOverlap(t *testing.T) {
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	gh, err := c.ClonePath("gitlab", "gitlab.com")
	if err != nil {
		t.Fatal(err)
	}
	gl, err := c.ClonePath("@gitlab/gitlab.com", "42")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(gl, gh+string(filepath.Separator)) || strings.HasPrefix(gh, gl+string(filepath.Separator)) || gh == gl {
		t.Errorf("GitHub clone %q and GitLab clone %q overlap", gh, gl)
	}
}

func TestRemote_DefaultGuardRefusesLinkLocalAndUnspecified(t *testing.T) {
	for _, a := range []string{"169.254.169.254", "::ffff:169.254.169.254", "0.0.0.0", "::", "fe80::1"} {
		var asked []string
		c := newClient(t, gitcli.Options{CloneRoot: t.TempDir(), Resolver: resolveTo(&asked, a)})
		rem := gitcli.Remote{URL: "https://gitlab.example.org/g/p.git", Prefix: "https://gitlab.example.org/"}
		if _, err := c.CloneRemote(testCtx(t), glOwnerKey, "1", rem, gitcli.CloneOptions{}); !errors.Is(err, gitcli.ErrForbiddenAddress) {
			t.Errorf("CloneRemote() with %s error = %v, want ErrForbiddenAddress", a, err)
		}
	}
}
