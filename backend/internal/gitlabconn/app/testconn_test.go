package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/gitlab"
)

const (
	testToken = "glpat-abcdefghijklmnop"
	baseURL   = "https://gitlab.corp.example/gitlab"
)

// testEnv is an env with one connection that has a token.
func testEnv() *env {
	e := newEnv()
	e.seed(id1, "Corp", baseURL, "gitlab.corp.example", testToken)
	return e
}

func ptr[T any](v T) *T { return &v }

func TestTestConnection_Success(t *testing.T) {
	e := testEnv()
	e.gl.tokenSelf = func(context.Context) (gitlab.TokenInfo, error) {
		return gitlab.TokenInfo{Scopes: []string{"api", "read_repository"}, ExpiresAt: "2026-11-25"}, nil
	}
	got, err := e.svc.TestConnection(t.Context(), id1, "")
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	want := app.TestResult{
		OK: true, Message: "Connected as @ann", LatencyMS: ptr(0), Login: ptr("ann"),
		Scopes: []string{"api", "read_repository"}, ExpiresAt: ptr("2026-11-25"), InstanceVersion: ptr("17.5.1-ee"),
		TokenAccess: domain.AccessFull, Warnings: []domain.Warning{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("TestConnection() mismatch (-want +got):\n%s", diff)
	}

	// AC6: exactly the three calls, in order, on the REST root with the stored token.
	calls := e.gl.seen()
	var ops []string
	for _, c := range calls {
		ops = append(ops, c.op)
		if c.conn != (gitlab.Conn{BaseURL: baseURL + "/api/v4", Token: testToken}) {
			t.Errorf("%s was called with %+v, want the REST root and the stored token", c.op, c.conn)
		}
		if c.deadline <= 0 || c.deadline > gitlab.TestTimeout {
			t.Errorf("%s ctx had %v left, want a deadline within gitlab.TestTimeout (%v)", c.op, c.deadline, gitlab.TestTimeout)
		}
	}
	if diff := cmp.Diff([]string{"user", "token_self", "metadata"}, ops); diff != "" {
		t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
}

func TestTestConnection_Evaluation(t *testing.T) {
	day := func(n int) string {
		return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n).Format(time.DateOnly)
	}
	tests := []struct {
		name      string
		scopes    []string // nil with scopesErr: the call fails
		scopesErr error
		expires   string
		version   string
		metaErr   error
		wantOK    bool
		wantMsg   string
		wantAcc   domain.TokenAccess
		wantCodes []string
	}{
		{"read_api+read_repository", []string{"read_api", "read_repository"}, nil, "", "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessReadOnly, []string{"read_only_token"}},
		{"api", []string{"api"}, nil, "", "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessFull, nil},
		{"read_api only", []string{"read_api"}, nil, "", "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessReadOnly, []string{"read_only_token", "no_repository_scope"}},
		{"read_user", []string{"read_user"}, nil, "", "17.5.1-ee", nil, false, domain.MsgNeedsScope, "", nil},
		{"empty scopes", []string{}, nil, "", "17.5.1-ee", nil, false, domain.MsgNeedsScope, "", nil},
		{"self 400", nil, &gitlab.StatusError{Status: 400, Message: "bad"}, "", "17.5.1-ee", nil, true, "Connected as @ann", "", []string{"scopes_unknown"}},
		{"expires in 14 days", []string{"api"}, nil, day(14), "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessFull, []string{"token_expiring"}},
		{"expires in 15 days", []string{"api"}, nil, day(15), "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessFull, nil},
		{"unreadable expiry", []string{"api"}, nil, "soon", "17.5.1-ee", nil, true, "Connected as @ann", domain.AccessFull, nil},
		{"old version", []string{"api"}, nil, "", "16.11.3-ee", nil, true, "Connected as @ann", domain.AccessFull, []string{"version_unsupported"}},
		{"17.0.0", []string{"api"}, nil, "", "17.0.0", nil, true, "Connected as @ann", domain.AccessFull, nil},
		{"metadata 403", []string{"api"}, nil, "", "", &gitlab.StatusError{Status: 403}, true, "Connected as @ann", domain.AccessFull, []string{"version_unknown"}},
		{"everything unknown", nil, errors.New("down"), "", "", errors.New("down"), true, "Connected as @ann", "", []string{"scopes_unknown", "version_unknown"}},
		{"all warnings in order", []string{"read_api"}, nil, day(3), "16.0.0", nil, true, "Connected as @ann", domain.AccessReadOnly,
			[]string{"read_only_token", "no_repository_scope", "token_expiring", "version_unsupported"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv()
			e.gl.tokenSelf = func(context.Context) (gitlab.TokenInfo, error) {
				if tc.scopesErr != nil {
					return gitlab.TokenInfo{}, tc.scopesErr
				}
				return gitlab.TokenInfo{Scopes: tc.scopes, ExpiresAt: tc.expires}, nil
			}
			e.gl.metadata = func(context.Context) (gitlab.Metadata, error) {
				return gitlab.Metadata{Version: tc.version}, tc.metaErr
			}
			got, err := e.svc.TestConnection(t.Context(), id1, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			var codes []string
			for _, w := range got.Warnings {
				codes = append(codes, w.Code)
			}
			if got.OK != tc.wantOK || got.Message != tc.wantMsg || got.TokenAccess != tc.wantAcc || !cmp.Equal(codes, tc.wantCodes) {
				t.Errorf("TestConnection() = ok %v msg %q access %q warnings %v, want ok %v msg %q access %q warnings %v",
					got.OK, got.Message, got.TokenAccess, codes, tc.wantOK, tc.wantMsg, tc.wantAcc, tc.wantCodes)
			}
			if !tc.wantOK {
				if diff := cmp.Diff(app.TestResult{Message: tc.wantMsg}, got); diff != "" {
					t.Errorf("a failed test must leave every other field null (-want +got):\n%s", diff)
				}
				return
			}
			if got.Warnings == nil || got.LatencyMS == nil || got.Login == nil {
				t.Errorf("TestConnection() = %+v, want non-nil warnings, latency and login on success", got)
			}
			if (tc.scopesErr != nil) != (got.Scopes == nil) {
				t.Errorf("scopes = %v, want nil exactly when the call failed", got.Scopes)
			}
		})
	}
}

func TestTestConnection_ExpiryDate(t *testing.T) {
	e := testEnv()
	e.gl.tokenSelf = func(context.Context) (gitlab.TokenInfo, error) {
		return gitlab.TokenInfo{Scopes: []string{"api"}, ExpiresAt: "2026-11-02T00:00:00.000Z"}, nil
	}
	got, _ := e.svc.TestConnection(t.Context(), id1, "")
	if got.ExpiresAt == nil || *got.ExpiresAt != "2026-11-02" {
		t.Errorf("ExpiresAt = %v, want 2026-11-02", got.ExpiresAt)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Message != "Token expires on 2026-11-02 (in 7 days)" {
		t.Errorf("Warnings = %+v, want the 7-day expiry warning", got.Warnings)
	}
}

func TestTestConnection_UserFailures(t *testing.T) {
	forbidden := fmt.Errorf("dial: %w", egress.ErrForbiddenAddr)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"401", &gitlab.StatusError{Status: 401, Message: "401 Unauthorized"}, "GitLab rejected the token (401): it is wrong, expired or revoked"},
		{"403", &gitlab.StatusError{Status: 403}, "The token cannot read its user (403)"},
		{"redirect", fmt.Errorf("gitlab user: %w", gitlab.ErrRedirect), "GitLab answered with a redirect — check the base URL"},
		{"html page", fmt.Errorf("gitlab user: %w", gitlab.ErrNotAPI), "No GitLab API found at " + baseURL},
		{"404", &gitlab.StatusError{Status: 404}, "No GitLab API found at " + baseURL},
		{"500", &gitlab.StatusError{Status: 500}, "GitLab is not reachable at " + baseURL},
		{"429", &gitlab.StatusError{Status: 429}, "GitLab is not reachable at " + baseURL},
		{"connection refused", errors.New("dial tcp: connection refused"), "GitLab is not reachable at " + baseURL},
		{"deadline", context.DeadlineExceeded, "GitLab is not reachable at " + baseURL},
		{"forbidden address", forbidden, egress.ForbiddenMessage},
		{"bad token characters", gitlab.ErrToken, "The token contains characters that cannot be sent to GitLab"},
		{"401 echoing the token", &gitlab.StatusError{Status: 401, Message: "bad " + testToken}, "GitLab rejected the token (401): it is wrong, expired or revoked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv()
			e.gl.user = func(context.Context) (gitlab.User, error) { return gitlab.User{}, tc.err }
			got, err := e.svc.TestConnection(t.Context(), id1, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v, want the failure as data", err)
			}
			if diff := cmp.Diff(app.TestResult{Message: tc.want}, got); diff != "" {
				t.Errorf("TestConnection() mismatch (-want +got):\n%s", diff)
			}
			if ops := e.gl.ops(); len(ops) != 1 {
				t.Errorf("GitLab calls = %v, want only /user after it failed", ops)
			}
		})
	}
}

func TestTestConnection_NoToken(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", baseURL, "gitlab.corp.example", "")
	got, err := e.svc.TestConnection(t.Context(), id1, "")
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	if diff := cmp.Diff(app.TestResult{Message: "GitLab connection Corp has no token"}, got); diff != "" {
		t.Errorf("TestConnection() mismatch (-want +got):\n%s", diff)
	}
	if ops := e.gl.ops(); len(ops) != 0 {
		t.Errorf("GitLab calls = %v, want none without a token (FR9)", ops)
	}
}

func TestTestConnection_KeyIsSavedFirstAndKept(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", baseURL, "gitlab.corp.example", "")
	var tokenSeen string
	e.gl.user = func(context.Context) (gitlab.User, error) {
		tokenSeen = e.secrets.GitLab(id1)
		return gitlab.User{}, &gitlab.StatusError{Status: 401, Message: "401 Unauthorized"}
	}
	got, err := e.svc.TestConnection(t.Context(), id1, "glpat-freshfreshfresh")
	if err != nil || got.OK {
		t.Fatalf("TestConnection() = %+v, %v, want a failed test as data", got, err)
	}
	if tokenSeen != "glpat-freshfreshfresh" {
		t.Errorf("the key was saved as %q when /user ran, want it saved before the test", tokenSeen)
	}
	if e.secrets.GitLab(id1) != "glpat-freshfreshfresh" {
		t.Errorf("stored token after a 401 = %q, want the key kept (FR9)", e.secrets.GitLab(id1))
	}
	if calls := e.gl.seen(); calls[0].conn.Token != "glpat-freshfreshfresh" {
		t.Errorf("the test used token %q, want the key", calls[0].conn.Token)
	}
}

func TestTestConnection_KeyReplacesStoredToken(t *testing.T) {
	e := testEnv()
	if _, err := e.svc.TestConnection(t.Context(), id1, "glpat-freshfreshfresh"); err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	if e.secrets.GitLab(id1) != "glpat-freshfreshfresh" {
		t.Errorf("stored token = %q, want the key", e.secrets.GitLab(id1))
	}
}

func TestTestConnection_Errors(t *testing.T) {
	e := testEnv()
	for _, id := range []string{id2, "nope", ""} {
		_, err := e.svc.TestConnection(t.Context(), id, "glpat-freshfreshfresh")
		wantErr(t, err, apperr.KindNotFound, "gitlab_connection_not_found")
	}
	if len(e.secrets.tokens) != 1 || e.secrets.tokens[id1] != testToken || len(e.gl.ops()) != 0 {
		t.Errorf("an unknown id saved %v or called GitLab %v, want nothing (AC6)", e.secrets.tokens, e.gl.ops())
	}

	_, err := e.svc.TestConnection(t.Context(), id1, "has space")
	ae := wantErr(t, err, apperr.KindValidation, "validation_error")
	if diff := cmp.Diff([]string{"[key]: Token must not contain whitespace or control characters"}, issues(t, ae)); diff != "" {
		t.Errorf("issues mismatch (-want +got):\n%s", diff)
	}
	if e.secrets.tokens[id1] != testToken {
		t.Errorf("an invalid key replaced the token")
	}

	e.secrets.setErr = errBoom
	if _, err := e.svc.TestConnection(t.Context(), id1, "glpat-freshfreshfresh"); !errors.Is(err, errBoom) {
		t.Errorf("TestConnection() with a failing secrets write: error = %v, want it wrapped", err)
	}
	if len(e.gl.ops()) != 0 {
		t.Errorf("GitLab was called although the key could not be saved")
	}
}

func TestTestConnection_NoSecretInResults(t *testing.T) {
	e := newEnv()
	// A name that holds a token shape, a login and version that echo the token.
	e.seed(id1, "glpat-namenamenamename", baseURL+"/"+url.PathEscape(testToken), "gitlab.corp.example", testToken)
	e.gl.user = func(context.Context) (gitlab.User, error) { return gitlab.User{Username: "ann-" + testToken}, nil }
	e.gl.metadata = func(context.Context) (gitlab.Metadata, error) {
		return gitlab.Metadata{Version: "17.0 " + testToken}, nil
	}
	got, err := e.svc.TestConnection(t.Context(), id1, "")
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	if !strings.Contains(got.Message, "[REDACTED]") {
		t.Errorf("Message = %q, want the echoed token masked", got.Message)
	}
	for name, v := range map[string]*string{"Login": got.Login, "InstanceVersion": got.InstanceVersion} {
		if v == nil || strings.Contains(*v, "abcdefghijklmnop") {
			t.Errorf("%s = %v, want it present with the token masked (NFR1)", name, v)
		}
	}

	e.gl.user = func(context.Context) (gitlab.User, error) {
		return gitlab.User{}, errors.New("Get " + baseURL + ": " + testToken)
	}
	got, _ = e.svc.TestConnection(t.Context(), id1, "")
	if strings.Contains(got.Message, "abcdefghijklmnop") {
		t.Errorf("failure message = %q, want no token", got.Message)
	}

	// The no-token message carries the connection name, which is user text.
	e.secrets.tokens = map[string]string{}
	e.store.put(domain.Connection{ID: id2, Name: "glpat-namenamenamename", BaseURL: "https://x.example", Host: "x.example"})
	got, _ = e.svc.TestConnection(t.Context(), id2, "")
	if strings.Contains(got.Message, "namenamename") {
		t.Errorf("no-token message = %q, want a token-shaped name masked", got.Message)
	}
}

func TestTestConnection_LoginAndScopesAreBounded(t *testing.T) {
	e := testEnv()
	e.gl.user = func(context.Context) (gitlab.User, error) {
		return gitlab.User{Username: "an\nn" + strings.Repeat("x", 500)}, nil
	}
	many := make([]string, 80)
	for i := range many {
		many[i] = "api"
	}
	e.gl.tokenSelf = func(context.Context) (gitlab.TokenInfo, error) { return gitlab.TokenInfo{Scopes: many}, nil }
	got, _ := e.svc.TestConnection(t.Context(), id1, "")
	if got.Login == nil || len(*got.Login) > 200 || strings.ContainsAny(*got.Login, "\n") {
		t.Errorf("Login = %+v, want control characters dropped and at most 200 bytes", got)
	}
	if len(got.Scopes) != 50 {
		t.Errorf("len(Scopes) = %d, want 50", len(got.Scopes))
	}
}

func TestTestConnection_CanceledContextIsAnError(t *testing.T) {
	for _, stage := range []string{"user", "token_self", "metadata"} {
		t.Run(stage, func(t *testing.T) {
			e := testEnv()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fail := func() error { cancel(); return context.Canceled }
			switch stage {
			case "user":
				e.gl.user = func(context.Context) (gitlab.User, error) { return gitlab.User{}, fail() }
			case "token_self":
				e.gl.tokenSelf = func(context.Context) (gitlab.TokenInfo, error) { return gitlab.TokenInfo{}, fail() }
			case "metadata":
				e.gl.metadata = func(context.Context) (gitlab.Metadata, error) { return gitlab.Metadata{}, fail() }
			}
			if _, err := e.svc.TestConnection(ctx, id1, ""); !errors.Is(err, context.Canceled) {
				t.Errorf("TestConnection() error = %v, want context.Canceled (not a failed-test result)", err)
			}
		})
	}
}

// Each of the three calls gets its own gitlab.TestTimeout: a call that hangs is cut off after it,
// and a slow /user does not eat the budget of the next call.
func TestTestConnection_TimeoutPerCall(t *testing.T) {
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

	t.Run("user hangs", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			e := testEnv()
			e.gl.user = func(ctx context.Context) (gitlab.User, error) { return gitlab.User{}, hang(ctx) }
			start := time.Now()
			got, err := e.svc.TestConnection(t.Context(), id1, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			if want := "GitLab is not reachable at " + baseURL; got.OK || got.Message != want {
				t.Errorf("TestConnection() = %+v, want %q", got, want)
			}
			if took := time.Since(start); took != gitlab.TestTimeout {
				t.Errorf("took %v, want exactly gitlab.TestTimeout", took)
			}
		})
	})
	t.Run("slow calls each get a full budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			e := testEnv()
			slow := gitlab.TestTimeout - time.Second
			e.gl.user = func(ctx context.Context) (gitlab.User, error) {
				time.Sleep(slow)
				return gitlab.User{Username: "ann"}, ctx.Err()
			}
			e.gl.tokenSelf = func(ctx context.Context) (gitlab.TokenInfo, error) {
				time.Sleep(slow)
				return gitlab.TokenInfo{Scopes: []string{"api"}}, ctx.Err()
			}
			got, err := e.svc.TestConnection(t.Context(), id1, "")
			if err != nil || !got.OK || got.TokenAccess != domain.AccessFull {
				t.Errorf("TestConnection() = %+v, %v, want OK with scopes read after two slow calls", got, err)
			}
		})
	})
	t.Run("optional calls hang", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			e := testEnv()
			e.gl.tokenSelf = func(ctx context.Context) (gitlab.TokenInfo, error) { return gitlab.TokenInfo{}, hang(ctx) }
			e.gl.metadata = func(ctx context.Context) (gitlab.Metadata, error) { return gitlab.Metadata{}, hang(ctx) }
			got, err := e.svc.TestConnection(t.Context(), id1, "")
			if err != nil || !got.OK {
				t.Fatalf("TestConnection() = %+v, %v, want OK with unknowns", got, err)
			}
			var codes []string
			for _, w := range got.Warnings {
				codes = append(codes, w.Code)
			}
			if diff := cmp.Diff([]string{"scopes_unknown", "version_unknown"}, codes); diff != "" {
				t.Errorf("warnings mismatch (-want +got):\n%s", diff)
			}
		})
	})
}

func TestTestConnection_LatencyUsesTheClock(t *testing.T) {
	e := testEnv()
	step := 0
	e.svc = app.NewService(app.Deps{Store: e.store, Secrets: e.secrets, Repos: e.repos, GitLab: e.gl,
		Clock: func() time.Time { step++; return e.now.Add(time.Duration(step) * 40 * time.Millisecond) }})
	got, err := e.svc.TestConnection(t.Context(), id1, "")
	if err != nil || got.LatencyMS == nil || *got.LatencyMS < 40 {
		t.Errorf("TestConnection() = %+v, %v, want latency_ms >= 40", got, err)
	}
}

func TestTestConnection_LogsOneLineWithProviderHostOkAndLatency(t *testing.T) { // AC8, NFR9
	tests := []struct {
		name   string
		token  string
		fail   bool
		wantOK string
	}{
		{name: "connected", token: testToken, wantOK: "ok=true"},
		{name: "user call fails", token: testToken, fail: true, wantOK: "ok=false"},
		{name: "no token", token: "", wantOK: "ok=false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			e.seed(id1, "Corp", baseURL, "gitlab.corp.example", tc.token)
			var logs bytes.Buffer
			e.svc = app.NewService(app.Deps{Store: e.store, Secrets: e.secrets, Repos: e.repos, GitLab: e.gl,
				Clock: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(&logs, nil))})
			if tc.fail {
				e.gl.user = func(context.Context) (gitlab.User, error) { return gitlab.User{}, errBoom }
			}
			if _, err := e.svc.TestConnection(t.Context(), id1, ""); err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			var test []string
			for _, l := range lines {
				if strings.Contains(l, "level=INFO") && strings.Contains(l, "connection test") {
					test = append(test, l)
				}
			}
			if len(test) != 1 {
				t.Fatalf("INFO test lines = %d (%q), want exactly 1", len(test), test)
			}
			for _, want := range []string{"provider=gitlab", "host=gitlab.corp.example", tc.wantOK, "latency_ms="} {
				if !strings.Contains(test[0], want) {
					t.Errorf("test log line %q lacks %q", test[0], want)
				}
			}
			if strings.Contains(logs.String(), testToken) {
				t.Errorf("log %q holds the token", logs.String())
			}
		})
	}
}
