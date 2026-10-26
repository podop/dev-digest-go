package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/repos/domain"
)

// Wire codes of the GitLab add (spec FR13-FR15, FR48).
const (
	CodeHostNotConnected = "repo_host_not_connected"
	CodeProjectNotFound  = "gitlab_project_not_found"
	CodePermissionDenied = "gitlab_permission_denied"
	CodeRateLimited      = "gitlab_rate_limited"
	CodeUnavailable      = "gitlab_unavailable"
	CodePathConflict     = "gitlab_path_conflict"
)

// GitLabConn is one GitLab connection as the repos context needs it. BaseURL is the
// normalised web address (https://host[:port][/prefix], no trailing slash, no /api/v4); Host is
// the connection's host key. Token is "" for an anonymous connection and never leaves this
// process: String, GoString and LogValue omit it.
type GitLabConn struct {
	ID      string
	Name    string
	BaseURL string
	Host    string
	Token   string
}

// String omits the token, so a GitLabConn printed with %v or %+v cannot leak it.
func (c GitLabConn) String() string { return "GitLabConn{" + c.Name + " " + c.BaseURL + "}" }

// GoString omits the token, so %#v cannot leak it.
func (c GitLabConn) GoString() string { return c.String() }

// LogValue omits the token for slog.
func (c GitLabConn) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", c.ID), slog.String("host", c.Host))
}

// Connections resolves GitLab connections by host key (spec FR48: binding happens at every
// call, repositories store no connection reference). The gitlabconn use case implements it
// behind an adapter in cmd.
type Connections interface {
	// Resolve returns the connection whose host key equals hostKey, with its token; ok is false
	// when there is none.
	Resolve(ctx context.Context, hostKey string) (conn GitLabConn, ok bool, err error)
	// HostKeysByName returns the host keys of every connection whose host name (the host key
	// without its port) equals name. An SSH URL names a host without a port (FR13).
	HostKeysByName(ctx context.Context, name string) ([]string, error)
}

// GitLabProjects is the GitLab access the add needs; *gitlab.Client implements it. The add makes
// exactly one call (FR14), never follows a redirect and passes the connection's token only to
// the connection's own address.
type GitLabProjects interface {
	// ProjectByPath reads GET /projects/<URL-encoded path>. conn.BaseURL is the REST root
	// (<base>/api/v4). It fails with *gitlab.StatusError, gitlab.ErrRedirect or gitlab.ErrNotAPI.
	ProjectByPath(ctx context.Context, conn gitlab.Conn, path string) (gitlab.Project, error)
}

var _ GitLabProjects = (*gitlab.Client)(nil)

// ProjectPath is a GitLab project's current path (spec FR47) as stored on its repository.
type ProjectPath struct {
	FullName string // path_with_namespace
	Owner    string // namespace full path
	Name     string // project path
	WebURL   string // <base_url>/<full name>, built from the connection
}

// NotConnectedError means no GitLab connection has the repository's host key (spec FR48). Its
// text is the client-facing message, so a failed job shows the same words as the 422.
type NotConnectedError struct {
	Host string
}

func (e *NotConnectedError) Error() string {
	return fmt.Sprintf("No GitLab connection for %s. Add one in Settings → GitLab Integration.", e.Host)
}

// notConnected is the 422 repo_host_not_connected of the add.
func notConnected(host string) *apperr.Error {
	err := &NotConnectedError{Host: host}
	return apperr.Validation(err.Error()).WithCode(CodeHostNotConnected).
		WithDetails(map[string]string{"host": host}).WithCause(err)
}

// urlIssue is the 422 validation_error located at ["url"], in the shape httpx.NewValidationError
// renders (the app layer does not import the HTTP package).
func urlIssue(message string) *apperr.Error {
	type body struct {
		Code    string `json:"code"`
		Path    []any  `json:"path"`
		Message string `json:"message"`
	}
	type detail struct {
		Keyword      string `json:"keyword"`
		InstancePath string `json:"instancePath"`
		SchemaPath   string `json:"schemaPath"`
		Message      string `json:"message"`
		Params       struct {
			Issue body `json:"issue"`
		} `json:"params"`
	}
	d := detail{Keyword: "custom", InstancePath: "/url", SchemaPath: "#/url/custom", Message: message}
	d.Params.Issue = body{Code: "custom", Path: []any{"url"}, Message: message}
	return apperr.Validation("Request validation failed").WithDetails([]detail{d})
}

// apiSuffix is appended to a connection's base URL to get its REST root.
const apiSuffix = "/api/v4"

// addGitLab is Add for a GitLab URL (spec FR12-FR15, FR47): bind the host to a connection, cut
// the connection's path prefix, read the project once with the connection's token, then store
// it under the project's canonical path. Nothing is stored and no job is enqueued on any error.
func (s *Service) addGitLab(ctx context.Context, workspaceID, userID string, parsed domain.Parsed) (domain.Repo, bool, error) {
	conn, err := s.bindConnection(ctx, parsed)
	if err != nil {
		// Without a connection there is no path prefix to cut, so the project path limits of
		// FR12 hold for the whole path and a bad one is a validation error, not "not connected"
		// (EC12).
		var nc *NotConnectedError
		if errors.As(err, &nc) {
			if _, lerr := parsed.WithoutPrefix(""); lerr != nil {
				return domain.Repo{}, false, urlIssue(domain.InvalidURLMessage)
			}
		}
		return domain.Repo{}, false, err
	}
	rest, err := parsed.WithoutPrefix(pathPrefix(conn.BaseURL))
	switch {
	case errors.Is(err, domain.ErrOutsidePrefix):
		return domain.Repo{}, false, urlIssue("This URL is outside " + conn.BaseURL)
	case err != nil:
		return domain.Repo{}, false, urlIssue(domain.InvalidURLMessage)
	}
	if s.projects == nil {
		return domain.Repo{}, false, notConnected(parsed.Host)
	}
	project, err := s.projects.ProjectByPath(ctx,
		gitlab.Conn{BaseURL: conn.BaseURL + apiSuffix, Token: conn.Token}, rest.Path)
	if err != nil {
		return domain.Repo{}, false, mapProjectError(ctx, err, conn, rest.Path)
	}
	ref, err := canonicalRef(project)
	if err != nil {
		return domain.Repo{}, false, unavailable(err)
	}
	branch := project.DefaultBranch
	if branch == "" {
		branch = domain.DefaultBranch
	}
	// The address is built from the connection, never taken from the response (FR41).
	webURL := codehost.WebURL(conn.BaseURL, ref.FullName())
	repo, created, err := s.store.Add(ctx, NewRepo{
		WorkspaceID: workspaceID, CreatedBy: userID, Ref: ref,
		Provider: codehost.GitLab, Host: conn.Host, ProjectID: project.ID, WebURL: webURL,
		DefaultBranch: branch,
	})
	if err != nil {
		return domain.Repo{}, false, fmt.Errorf("add repo %s: %w", ref.FullName(), err)
	}
	if created {
		return repo, true, nil
	}
	if repo.ProjectID != project.ID {
		// Same host and full name, another project id: the stored row is the stale path of a
		// project that was renamed or moved (FR15).
		return domain.Repo{}, false, apperr.Conflict(fmt.Sprintf(
			"%s in this workspace is a different GitLab project that was renamed or moved; sync it first",
			ref.FullName())).WithCode(CodePathConflict).WithDetails(map[string]string{"repo_id": repo.ID})
	}
	repo, err = s.followPath(ctx, workspaceID, repo, ref, webURL)
	if err != nil {
		return domain.Repo{}, false, err
	}
	return repo, false, nil
}

// followPath stores the project's current path on an existing repo (FR47). A path another repo
// of the workspace already holds is skipped with one WARN; the next fetch tries again.
func (s *Service) followPath(ctx context.Context, workspaceID string, repo domain.Repo, ref domain.Ref, webURL string) (domain.Repo, error) {
	if repo.FullName == ref.FullName() && repo.WebURL == webURL {
		return repo, nil
	}
	p := ProjectPath{FullName: ref.FullName(), Owner: ref.Owner, Name: ref.Name, WebURL: webURL}
	other, err := s.store.UpdatePath(ctx, workspaceID, repo.ID, p)
	if err != nil {
		return domain.Repo{}, notFoundOr(err, "update path of repo "+repo.ID)
	}
	if other != "" {
		s.log.WarnContext(ctx, "gitlab project path not updated: another repo of the workspace has it",
			"repo", repo.ID, "other", other, "host", repo.Host)
		return repo, nil
	}
	repo.FullName, repo.Owner, repo.Name, repo.WebURL = p.FullName, p.Owner, p.Name, p.WebURL
	return repo, nil
}

// bindConnection finds the connection of a parsed GitLab URL (FR13). An HTTPS URL carries the
// host key; an SSH URL only the host name, which must name exactly one connection.
func (s *Service) bindConnection(ctx context.Context, parsed domain.Parsed) (GitLabConn, error) {
	if s.conns == nil {
		return GitLabConn{}, notConnected(parsed.Host)
	}
	key := parsed.Host
	if parsed.SSH {
		keys, err := s.conns.HostKeysByName(ctx, parsed.Host)
		if err != nil {
			return GitLabConn{}, fmt.Errorf("find gitlab connections for %s: %w", parsed.Host, err)
		}
		switch len(keys) {
		case 0:
			return GitLabConn{}, notConnected(parsed.Host)
		case 1:
			key = keys[0]
		default:
			return GitLabConn{}, urlIssue(fmt.Sprintf("Several GitLab connections use %s; paste the HTTPS URL", parsed.Host))
		}
	}
	conn, ok, err := s.conns.Resolve(ctx, key)
	if err != nil {
		return GitLabConn{}, fmt.Errorf("resolve gitlab connection for %s: %w", key, err)
	}
	if !ok {
		return GitLabConn{}, notConnected(parsed.Host)
	}
	return conn, nil
}

// pathPrefix returns the path of a connection's base URL ("" or "/gitlab").
func pathPrefix(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Path
}

// canonicalRef takes owner and name from the project's own answer and refuses an answer whose
// path is not a plain GitLab path or contradicts itself (codehost.ProjectRef): the path ends up in
// a clone URL and in the database, and the server is only as trustworthy as its connection.
func canonicalRef(p gitlab.Project) (domain.Ref, error) {
	owner, name, err := codehost.ProjectRef(p.PathWithNamespace, p.Path, p.NamespaceFullPath)
	if err != nil {
		return domain.Ref{}, fmt.Errorf("project path %q: %w", p.PathWithNamespace, err)
	}
	return domain.Ref{Owner: owner, Name: name}, nil
}

// mapProjectError turns a failed project read into the client-facing error (FR14). A context
// that ended is not GitLab's fault and is returned as it is.
func mapProjectError(ctx context.Context, err error, conn GitLabConn, path string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("read gitlab project %s: %w", path, err)
	}
	if errors.Is(err, gitlab.ErrRedirect) {
		return apperr.Validation(fmt.Sprintf("GitLab reports that %s has moved; paste the project's current URL", path)).
			WithCode(CodeProjectNotFound).WithCause(err)
	}
	var se *gitlab.StatusError
	if errors.As(err, &se) {
		switch {
		case se.IsNotFound():
			return apperr.Validation(fmt.Sprintf("Project %s was not found on %s, or the token cannot see it", path, conn.Host)).
				WithCode(CodeProjectNotFound).WithCause(err)
		case se.IsUnauthorized(), se.IsForbidden():
			return apperr.Validation(fmt.Sprintf("GitLab rejected the token of connection %s", conn.Name)).
				WithCode(CodePermissionDenied).WithCause(err)
		case se.IsRateLimited():
			return apperr.ExternalService("GitLab rate limit reached, try again later").
				WithCode(CodeRateLimited).WithCause(err)
		}
	}
	return unavailable(err)
}

func unavailable(err error) *apperr.Error {
	return apperr.ExternalService("GitLab is unavailable, try again later").WithCode(CodeUnavailable).WithCause(err)
}
