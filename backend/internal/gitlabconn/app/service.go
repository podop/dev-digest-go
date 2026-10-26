package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Clock returns the current time.
type Clock func() time.Time

// Deps are the ports of Service. Store, Secrets, Repos and GitLab are required; Clock defaults to
// time.Now and Log to a logger that discards.
type Deps struct {
	Store   Store
	Secrets Secrets
	Repos   RepoCounter
	GitLab  GitLab
	Clock   Clock
	Log     *slog.Logger
}

// Service implements the GitLab connection use cases.
type Service struct {
	d   Deps
	now Clock
	log *slog.Logger
	// writeMu serializes the writes that change a connection and its token (create, update,
	// delete, the key saved by a test), so the read-modify-write of the secrets file and its
	// restore after a failed commit never interleave. It is never held during a network call.
	writeMu sync.Mutex
}

// NewService builds the service from its ports; a nil required port is a wiring bug and panics.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Secrets == nil || d.Repos == nil || d.GitLab == nil {
		panic("gitlabconn: NewService needs Store, Secrets, Repos and GitLab")
	}
	s := &Service{d: d, now: d.Clock, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

// View is a connection as the API shows it (spec §7 GitLabConnection): the token is never part
// of it, only whether one is stored.
type View struct {
	Connection      domain.Connection
	TokenConfigured bool
	// RepoCount is the number of repositories of all workspaces bound to the host key (FR8).
	RepoCount int
}

// TokenOp says what a request does with the token (spec FR4).
type TokenOp int

// The token operations. The zero value keeps the token (field omitted).
const (
	// TokenKeep: `token` omitted. On create: no token.
	TokenKeep TokenOp = iota
	// TokenRemove: `token: null`. On create: no token.
	TokenRemove
	// TokenReplace: `token` is a string (Value); it is validated with domain.CheckToken.
	TokenReplace
)

// TokenInput is the token field of a create or update request.
type TokenInput struct {
	Op    TokenOp
	Value string
}

// CreateInput is the body of POST /settings/gitlab-connections. Nothing is trimmed here: the
// transport passes the values as received.
type CreateInput struct {
	// Name empty -> the host key (domain.CheckName).
	Name    string
	BaseURL string
	Token   TokenInput
}

// UpdateInput is the body of PUT /settings/gitlab-connections/:id: every field optional. A nil
// field keeps the stored value; a non-nil Name that is empty after trimming becomes the host key.
type UpdateInput struct {
	Name    *string
	BaseURL *string
	Token   TokenInput
}

// Conn is a connection with its token, as the other contexts use it (Resolve, All).
type Conn struct {
	ID   string
	Name string
	// BaseURL is the normalized instance URL (https://host[:port][/prefix]); append "/api/v4"
	// for the REST root (APIRoot) and build web and git addresses from it.
	BaseURL string
	// Host is the host key repositories bind by.
	Host string
	// Token is the stored token; "" when the connection has none (anonymous calls, or the
	// caller's NoTokenError).
	Token string
}

// APIRoot is the REST root of the connection: <BaseURL>/api/v4 (platform/gitlab.Conn.BaseURL).
func (c Conn) APIRoot() string { return c.BaseURL + "/api/v4" }

// String omits the token, so a Conn in a log line or an error never leaks it.
func (c Conn) String() string { return fmt.Sprintf("gitlab connection %s (%s)", c.ID, c.Host) }

// LogValue omits the token in structured logs.
func (c Conn) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", c.ID), slog.String("host", c.Host))
}

// List returns every connection sorted by name, then host, in byte order (spec FR3), each with
// token_configured and repo_count.
func (s *Service) List(ctx context.Context) ([]View, error) {
	list, err := s.d.Store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list gitlab connections: %w", err)
	}
	slices.SortFunc(list, func(a, b domain.Connection) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Host, b.Host))
	})
	hosts := make([]string, len(list))
	for i, c := range list {
		hosts[i] = c.Host
	}
	counts, err := s.countBound(ctx, hosts)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(list))
	for i, c := range list {
		out[i] = View{Connection: c, TokenConfigured: s.d.Secrets.GitLab(c.ID) != "", RepoCount: counts[c.Host]}
	}
	return out, nil
}

// Create validates the request and stores the connection and its token (spec FR1-FR4). The
// database write and the token write form one unit (P7): the token is written inside the
// transaction and put back when the commit fails. It makes no request to GitLab. Errors: 422
// validation_error (paths ["base_url"], ["name"], ["token"]), 422 gitlab_connection_limit, 409
// gitlab_connection_exists.
func (s *Service) Create(ctx context.Context, in CreateInput) (View, error) {
	var issues []issue
	norm, host, msg := domain.CheckBaseURL(in.BaseURL)
	if msg != "" {
		issues = append(issues, issue{path: []any{"base_url"}, message: msg})
	}
	var name string
	if host != "" || strings.TrimSpace(in.Name) != "" {
		var nmsg string
		if name, nmsg = domain.CheckName(in.Name, host); nmsg != "" {
			issues = append(issues, issue{path: []any{"name"}, message: nmsg})
		}
	}
	token := ""
	if in.Token.Op == TokenReplace {
		if tmsg := domain.CheckToken(in.Token.Value); tmsg != "" {
			issues = append(issues, issue{path: []any{"token"}, message: tmsg})
		}
		token = in.Token.Value
	}
	if len(issues) > 0 {
		return View{}, validationError(issues)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	c := domain.Connection{ID: newID(), Name: name, BaseURL: norm, Host: host, CreatedAt: now, UpdatedAt: now}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(c.ID)
	err := s.d.Store.InTx(ctx, func(ctx context.Context) error {
		if err := s.d.Store.Insert(ctx, c, domain.Limit); err != nil {
			return err
		}
		if token != "" {
			return u.set(token)
		}
		return nil
	})
	if err != nil {
		u.run(ctx)
		return View{}, s.storeError("create gitlab connection", c.ID, c.Host, err)
	}
	s.log.InfoContext(ctx, "gitlab connection created", "connection_id", c.ID, "host", c.Host,
		"token_configured", token != "")
	return View{Connection: c, TokenConfigured: token != ""}, nil
}

// Update changes name, base URL and token of a connection (spec FR4-FR6): a nil field is kept;
// `base_url` only with the same host key (else 422 at ["base_url"], MsgHostChange); the token
// follows TokenInput. The definition and the token change together or not at all. It makes no
// request to GitLab; the next call of every route, job and poll reads the new values (Resolve).
// Errors: 404 gitlab_connection_not_found, 422 validation_error.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (View, error) {
	if !validID(id) {
		return View{}, notFound(id)
	}
	var issues []issue
	var (
		norm, host string
		name       *string
	)
	if in.BaseURL != nil {
		var msg string
		if norm, host, msg = domain.CheckBaseURL(*in.BaseURL); msg != "" {
			issues = append(issues, issue{path: []any{"base_url"}, message: msg})
		}
	}
	token := ""
	if in.Token.Op == TokenReplace {
		if tmsg := domain.CheckToken(in.Token.Value); tmsg != "" {
			issues = append(issues, issue{path: []any{"token"}, message: tmsg})
		}
		token = in.Token.Value
	}
	if in.Name != nil {
		// Without a valid new base URL the stored host key is not known yet: a non-empty name is
		// checked now, an empty one (-> host key) inside the transaction.
		if trimmed := strings.TrimSpace(*in.Name); trimmed != "" {
			n, nmsg := domain.CheckName(*in.Name, "")
			if nmsg != "" {
				issues = append(issues, issue{path: []any{"name"}, message: nmsg})
			}
			name = &n
		}
	}
	if len(issues) > 0 {
		return View{}, validationError(issues)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(id)
	var out View
	err := s.d.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.d.Store.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		next := cur
		if in.BaseURL != nil {
			if host != cur.Host {
				return validationError([]issue{{path: []any{"base_url"}, message: MsgHostChange}})
			}
			next.BaseURL = norm
		}
		if in.Name != nil {
			if name != nil {
				next.Name = *name
			} else {
				n, nmsg := domain.CheckName("", cur.Host)
				if nmsg != "" {
					return validationError([]issue{{path: []any{"name"}, message: nmsg}})
				}
				next.Name = n
			}
		}
		next.UpdatedAt = s.now().UTC().Truncate(time.Millisecond)
		if err := s.d.Store.Replace(ctx, next); err != nil {
			return err
		}
		switch in.Token.Op {
		case TokenReplace:
			if err := u.set(token); err != nil {
				return err
			}
		case TokenRemove:
			if s.d.Secrets.GitLab(id) != "" {
				if err := u.set(""); err != nil {
					return err
				}
			}
		}
		counts, err := s.countBound(ctx, []string{next.Host})
		if err != nil {
			return err
		}
		out = View{Connection: next, TokenConfigured: s.d.Secrets.GitLab(id) != "", RepoCount: counts[next.Host]}
		return nil
	})
	if err != nil {
		u.run(ctx)
		return View{}, s.storeError("update gitlab connection", id, "", err)
	}
	s.log.InfoContext(ctx, "gitlab connection updated", "connection_id", id, "host", out.Connection.Host,
		"token_configured", out.TokenConfigured)
	return out, nil
}

// Delete removes the connection and its token together (spec FR7); it is allowed while
// repositories are bound to the host key: they become "not connected" and are counted in the
// returned disconnected. Errors: 404 gitlab_connection_not_found.
func (s *Service) Delete(ctx context.Context, id string) (disconnected int, err error) {
	if !validID(id) {
		return 0, notFound(id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	u := s.undo(id)
	var host string
	err = s.d.Store.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.d.Store.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		host = cur.Host
		counts, err := s.countBound(ctx, []string{cur.Host})
		if err != nil {
			return err
		}
		disconnected = counts[cur.Host]
		if err := s.d.Store.Delete(ctx, id); err != nil {
			return err
		}
		if s.d.Secrets.GitLab(id) != "" {
			return u.set("")
		}
		return nil
	})
	if err != nil {
		u.run(ctx)
		return 0, s.storeError("delete gitlab connection", id, "", err)
	}
	s.log.InfoContext(ctx, "gitlab connection deleted", "connection_id", id, "host", host,
		"disconnected_repos", disconnected)
	return disconnected, nil
}

// Resolve returns the connection for a host key with its current token (spec FR6: read at call
// time, so a rotated token applies to the next call). ok is false when no connection has that
// host key (the repository is "not connected", FR48); a connection without a token has
// Conn.Token "". The host key is matched exactly as stored.
func (s *Service) Resolve(ctx context.Context, host string) (Conn, bool, error) {
	c, err := s.d.Store.GetByHost(ctx, host)
	if errors.Is(err, domain.ErrNotFound) {
		return Conn{}, false, nil
	}
	if err != nil {
		return Conn{}, false, fmt.Errorf("resolve gitlab connection %s: %w", host, err)
	}
	return s.conn(c), true, nil
}

// All returns every connection with its token, sorted by host key in byte order.
func (s *Service) All(ctx context.Context) ([]Conn, error) {
	list, err := s.d.Store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list gitlab connections: %w", err)
	}
	slices.SortFunc(list, func(a, b domain.Connection) int { return strings.Compare(a.Host, b.Host) })
	out := make([]Conn, len(list))
	for i, c := range list {
		out[i] = s.conn(c)
	}
	return out, nil
}

func (s *Service) conn(c domain.Connection) Conn {
	return Conn{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Host: c.Host, Token: s.d.Secrets.GitLab(c.ID)}
}

func (s *Service) countBound(ctx context.Context, hosts []string) (map[string]int, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	counts, err := s.d.Repos.CountBound(ctx, hosts)
	if err != nil {
		return nil, fmt.Errorf("count repositories bound to gitlab connections: %w", err)
	}
	return counts, nil
}

// storeError maps the store's sentinels to application errors; an *apperr.Error passes unchanged
// and any other error is wrapped (a 500 at the edge). host is the host key of a create ("" when
// the error cannot be ErrExists).
func (s *Service) storeError(op, id, host string, err error) error {
	switch {
	case isAppErr(err):
		return err
	case errors.Is(err, domain.ErrNotFound):
		return notFound(id).WithCause(err)
	case errors.Is(err, domain.ErrExists):
		return exists(host, err)
	case errors.Is(err, domain.ErrLimit):
		return limitReached(err)
	}
	return fmt.Errorf("%s %s: %w", op, id, err)
}

func isAppErr(err error) bool {
	_, ok := apperr.As(err)
	return ok
}

// undo records a token write so that it can be put back when the database transaction that
// surrounds it does not commit (P7).
type undo struct {
	s     *Service
	id    string
	prev  string
	armed bool
}

func (s *Service) undo(id string) *undo { return &undo{s: s, id: id} }

// set writes token as the connection's token and arms the undo. An empty token removes the entry.
func (u *undo) set(token string) error {
	var (
		prev string
		err  error
	)
	if token == "" {
		prev, err = u.s.d.Secrets.DeleteGitLab(u.id)
	} else {
		prev, err = u.s.d.Secrets.SetGitLab(u.id, token)
	}
	if err != nil {
		return fmt.Errorf("write token of gitlab connection %s: %w", u.id, err)
	}
	u.prev, u.armed = prev, true
	return nil
}

// run puts the previous token back if a write was made. A failed restore is logged at ERROR with
// the connection id only; the secrets store never puts a value into an error.
func (u *undo) run(ctx context.Context) {
	if !u.armed {
		return
	}
	if err := u.s.d.Secrets.RestoreGitLab(u.id, u.prev); err != nil {
		u.s.log.ErrorContext(ctx, "gitlab connection token could not be restored after a failed write",
			"connection_id", u.id, "error", err.Error())
	}
}
