package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	gitlabconnpg "dev-digest-go/backend/internal/gitlabconn/adapters/postgres"
	gitlabconnapp "dev-digest-go/backend/internal/gitlabconn/app"
	gitlabconndomain "dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/secrets"
	pullsgitlab "dev-digest-go/backend/internal/pulls/adapters/gitlab"
	pullsdomain "dev-digest-go/backend/internal/pulls/domain"
	reposapp "dev-digest-go/backend/internal/repos/app"
	reposdomain "dev-digest-go/backend/internal/repos/domain"
	settingsapp "dev-digest-go/backend/internal/settings/app"
)

// GitLab A (docs/plans/2026-10-26-gitlab-a-connection-sync.md, S32): the composition root binds the
// gitlabconn context (global connections and their tokens) to the contexts that read a GitLab
// project: repos (add and clone), pulls (list, detail, poll), repo-intel (resync), settings
// (connection test) and automatic reviews (token facts). This file holds the adapters; the wiring
// is in wire (app.go).

// gitCABundleName is the file in the data directory that holds the system CA bundle plus the extra
// CA (DEVDIGEST_EXTRA_CA_FILE) for git's http.sslCAInfo (SCR5).
const gitCABundleName = "git-ca-bundle.pem"

// prepareGitCA writes <DataDir>/git-ca-bundle.pem when DEVDIGEST_EXTRA_CA_FILE is set and returns
// its path ("" = no extra CA, git keeps its default roots). git replaces its trust store with the
// bundle, so the bundle holds the system roots too; a host without a system bundle cannot make
// one, and that stops the start (SCR5) instead of silently leaving git without the extra CA.
// Errors name the variable and the path, never the certificate content.
//
// getenv is the process environment: SSL_CERT_FILE names the system bundle to use first.
func prepareGitCA(cfg config.Config, getenv func(string) string) (string, error) {
	return prepareGitCAWith(cfg, func(extra []byte, dest string) error {
		return gitcli.WriteCABundle(extra, dest, getenv("SSL_CERT_FILE"))
	})
}

// prepareGitCAWith is prepareGitCA over the bundle writer (a test seam: the host's own system
// bundle cannot be hidden from a test).
func prepareGitCAWith(cfg config.Config, write func(extra []byte, dest string) error) (string, error) {
	if cfg.ExtraCAFile == "" {
		return "", nil
	}
	extra, err := os.ReadFile(cfg.ExtraCAFile)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return "", fmt.Errorf("read extra CA file %q: %w", cfg.ExtraCAFile, err)
	}
	dest := filepath.Join(cfg.DataDir, gitCABundleName)
	if err := write(extra, dest); err != nil {
		if errors.Is(err, gitcli.ErrNoSystemBundle) {
			return "", fmt.Errorf("git needs a system CA bundle to add the extra CA to (install ca-certificates or set SSL_CERT_FILE): %w", err)
		}
		return "", fmt.Errorf("write git CA bundle %q: %w", dest, err)
	}
	return dest, nil
}

// gitCAFile returns the CAFile func of the repos jobs: nil (a true nil) without an extra CA.
func gitCAFile(path string) func() string {
	if path == "" {
		return nil
	}
	return func() string { return path }
}

// newGitLabHTTP is the HTTP client of every GitLab API call: no proxy (SCR2), no redirects, a
// connect-time guard that refuses link-local and unspecified addresses in every encoding
// (custom.ForbiddenAddr; loopback and private ones stay allowed, a self-hosted GitLab often runs
// there) and the extra CA besides the system store.
func newGitLabHTTP(rootCAs *x509.CertPool) *http.Client {
	return egress.Client{Guard: custom.ForbiddenAddr, RootCAs: rootCAs}.HTTPClient()
}

// extraRedactions are the secrets llm.Client.Redact masks besides the built-in keys: the custom
// providers' keys and header values and the GitLab tokens, read at every call so a rotation
// applies at once (NFR1, FR31).
func extraRedactions(sec *secrets.Store) func() []string {
	return func() []string { return slices.Concat(sec.CustomValues(), sec.GitLabValues()) }
}

// newGitLabConnections builds the connection use cases over the connection store (which also
// counts the bound repositories), the secrets file and the GitLab client.
func newGitLabConnections(db *gorm.DB, sec *secrets.Store, client *gitlab.Client, now func() time.Time, log *slog.Logger) *gitlabconnapp.Service {
	store := gitlabconnpg.NewStore(db)
	return gitlabconnapp.NewService(gitlabconnapp.Deps{
		Store:   store,
		Secrets: sec,
		Repos:   store,
		GitLab:  client,
		Clock:   gitlabconnapp.Clock(now),
		Log:     log,
	})
}

// connectionResolver is what the adapters below read from the gitlabconn use cases.
type connectionResolver interface {
	Resolve(ctx context.Context, host string) (gitlabconnapp.Conn, bool, error)
	All(ctx context.Context) ([]gitlabconnapp.Conn, error)
}

var _ connectionResolver = (*gitlabconnapp.Service)(nil)

// reposConnections adapts the connections to the repos context (add, clone and refresh jobs).
type reposConnections struct{ conns connectionResolver }

var _ reposapp.Connections = reposConnections{}

// Resolve returns the connection whose host key is hostKey, with its current token.
func (r reposConnections) Resolve(ctx context.Context, hostKey string) (reposapp.GitLabConn, bool, error) {
	c, ok, err := r.conns.Resolve(ctx, hostKey)
	if err != nil || !ok {
		return reposapp.GitLabConn{}, false, err
	}
	return reposapp.GitLabConn{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Host: c.Host, Token: c.Token}, true, nil
}

// HostKeysByName returns the host keys of every connection whose host name (the host key without
// its port) equals name, case-insensitively: an SSH URL names a host without a port (FR13).
func (r reposConnections) HostKeysByName(ctx context.Context, name string) ([]string, error) {
	all, err := r.conns.All(ctx)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, c := range all {
		if strings.EqualFold(hostName(c.Host), name) {
			keys = append(keys, c.Host)
		}
	}
	return keys, nil
}

// hostName cuts the port off a host key: "git.corp:8443" -> "git.corp", "[::1]:8443" -> "[::1]".
func hostName(hostKey string) string {
	if strings.HasPrefix(hostKey, "[") {
		if end := strings.IndexByte(hostKey, ']'); end >= 0 {
			return hostKey[:end+1]
		}
		return hostKey
	}
	if i := strings.LastIndexByte(hostKey, ':'); i >= 0 {
		return hostKey[:i]
	}
	return hostKey
}

// pullsResolver adapts the connections to the pulls GitLab adapter (read on every call, so a
// rotated token applies to the next request).
func pullsResolver(conns connectionResolver) pullsgitlab.Resolver {
	return pullsgitlab.ResolverFunc(func(ctx context.Context, host string) (pullsgitlab.Conn, bool, error) {
		c, ok, err := conns.Resolve(ctx, host)
		if err != nil || !ok {
			return pullsgitlab.Conn{}, false, err
		}
		return pullsgitlab.Conn{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Host: c.Host, Token: c.Token}, true, nil
	})
}

// bindingResolver adapts the connections to the platform GitLab client's lookup (gitlab.ResolveFunc):
// the host key finds the connection, with its token. The blast, brief, intent and skills adapters
// share it. It reads the connection at every call, so a rotated token applies to the next request
// and a removed connection or token stops the next one before any request is sent.
func bindingResolver(conns connectionResolver) gitlab.ResolveFunc {
	return func(ctx context.Context, hostKey string) (gitlab.Binding, bool, error) {
		c, ok, err := conns.Resolve(ctx, hostKey)
		if err != nil || !ok {
			return gitlab.Binding{}, false, err
		}
		return gitlab.Binding{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Host: c.Host, Token: c.Token}, true, nil
	}
}

// settingsGitLab adapts the connection test to the settings context (POST
// /settings/test-connection with provider gitlab).
type settingsGitLab struct {
	svc interface {
		TestConnection(ctx context.Context, id, key string) (gitlabconnapp.TestResult, error)
	}
}

var _ settingsapp.GitLabTester = settingsGitLab{}

// TestConnection runs the test of connection id. An unknown connection is the 404
// gitlab_connection_not_found (the use case answers it already; a bare domain.ErrNotFound from a
// store is mapped here too); a failed test is data.
func (s settingsGitLab) TestConnection(ctx context.Context, id, key string) (settingsapp.ConnTestResult, error) {
	res, err := s.svc.TestConnection(ctx, id, key)
	if err != nil {
		if errors.Is(err, gitlabconndomain.ErrNotFound) {
			if _, ok := apperr.As(err); !ok {
				return settingsapp.ConnTestResult{}, apperr.NotFound("GitLab connection " + id + " not found").
					WithCode(gitlabconnapp.CodeNotFound).WithCause(err)
			}
		}
		return settingsapp.ConnTestResult{}, err
	}
	out := settingsapp.ConnTestResult{
		OK: res.OK, Message: res.Message, LatencyMS: res.LatencyMS, Login: res.Login, Scopes: res.Scopes,
		ExpiresAt: res.ExpiresAt, InstanceVersion: res.InstanceVersion,
	}
	if res.TokenAccess != "" {
		access := string(res.TokenAccess)
		out.TokenAccess = &access
	}
	if res.OK {
		out.Warnings = make([]settingsapp.TestWarning, len(res.Warnings))
		for i, w := range res.Warnings {
			out.Warnings[i] = settingsapp.TestWarning{Code: w.Code, Message: w.Message}
		}
	}
	return out, nil
}

// repoLister lists the repositories of a workspace (*reposapp.Service).
type repoLister interface {
	List(ctx context.Context, workspaceID string) ([]reposdomain.Repo, error)
}

// tokenSource reads the connection of a host key with its token (*gitlabconnapp.Service).
type tokenSource interface {
	Resolve(ctx context.Context, host string) (gitlabconnapp.Conn, bool, error)
}

// pollTargets lists every repository of every workspace (the pulls store).
type pollTargets interface {
	ListPollTargets(ctx context.Context) ([]pullsdomain.PollTarget, error)
}

// intelGit is the git port of repo-intel (Head, DiffNames, DefaultBranch, Sync). The readers work
// on any clone key as they are; the two that reach the remote detect a GitLab clone key
// ("@gitlab/<host dir>", "<project id>") and fetch from the project's connection, with its token
// and the CA bundle, through the Remote methods (the project path is read from the repository
// rows: the clone key holds no path). Without it a GitLab resync would fail as sync_failed.
type intelGit struct {
	*gitcli.Client
	conns  tokenSource
	paths  pollTargets
	caFile func() string
}

// isGitLabKey reports whether owner is the first half of a GitLab clone key.
func isGitLabKey(owner string) bool { return strings.HasPrefix(owner, codehost.GitLabCloneRoot+"/") }

// DefaultBranch asks the remote for its default branch.
func (g intelGit) DefaultBranch(ctx context.Context, owner, name string) (string, error) {
	if !isGitLabKey(owner) {
		return g.Client.DefaultBranch(ctx, owner, name)
	}
	r, err := g.remote(ctx, owner, name)
	if err != nil {
		return "", err
	}
	return g.Client.DefaultBranchRemote(ctx, owner, name, r)
}

// Sync fetches branch, resets the clone to it and returns the new HEAD sha.
func (g intelGit) Sync(ctx context.Context, owner, name, branch string) (string, error) {
	if !isGitLabKey(owner) {
		return g.Client.Sync(ctx, owner, name, branch)
	}
	r, err := g.remote(ctx, owner, name)
	if err != nil {
		return "", err
	}
	return g.Client.SyncRemote(ctx, owner, name, branch, r)
}

// remote builds the Remote of a GitLab clone key: the project whose clone key it is (any
// workspace's repository row; the path is the same for all), bound to its connection now (FR48).
// A host without a connection is a *reposapp.NotConnectedError and no git command runs.
func (g intelGit) remote(ctx context.Context, owner, name string) (gitcli.Remote, error) {
	targets, err := g.paths.ListPollTargets(ctx)
	if err != nil {
		return gitcli.Remote{}, fmt.Errorf("find gitlab repository for clone %s/%s: %w", owner, name, err)
	}
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil || id <= 0 {
		return gitcli.Remote{}, fmt.Errorf("gitlab clone key %s/%s: not a project id", owner, name)
	}
	for _, t := range targets {
		r := t.Repo
		if !r.IsGitLab() || r.ProjectID != id {
			continue
		}
		if first, _ := codehost.CloneKey(codehost.GitLab, r.Host, r.ProjectID, r.Owner, r.Name); first != owner {
			continue
		}
		path := r.Owner + "/" + r.Name
		if p, err := reposdomain.ParseURL("https://gitlab.invalid/" + path); err != nil || p.Path != path {
			// The path goes into a URL: refuse anything that is not a plain project path.
			return gitcli.Remote{}, fmt.Errorf("gitlab repo %s: stored path is not a plain project path", r.ID)
		}
		conn, ok, err := g.conns.Resolve(ctx, r.Host)
		if err != nil {
			return gitcli.Remote{}, fmt.Errorf("resolve gitlab connection for %s: %w", r.Host, err)
		}
		if !ok {
			return gitcli.Remote{}, &reposapp.NotConnectedError{Host: r.Host}
		}
		base := strings.TrimRight(conn.BaseURL, "/")
		remote := gitcli.Remote{
			URL:    codehost.WebURL(base, path) + ".git",
			Prefix: base + "/",
			Token:  conn.Token,
		}
		if g.caFile != nil {
			remote.CAFile = g.caFile()
		}
		return remote, nil
	}
	return gitcli.Remote{}, fmt.Errorf("gitlab clone %s/%s: no repository row", owner, name)
}
