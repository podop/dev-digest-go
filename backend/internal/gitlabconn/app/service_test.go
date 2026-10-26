package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// wantErr checks that err is an *apperr.Error of kind with the wire code and returns it.
func wantErr(t *testing.T, err error, kind apperr.Kind, code string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an *apperr.Error of kind %v", err, kind)
	}
	if ae.Kind != kind || ae.Code != code {
		t.Fatalf("error = kind %v code %q (%v), want kind %v code %q", ae.Kind, ae.Code, ae, kind, code)
	}
	return ae
}

// issues returns the "path: message" of each detail of a validation error.
func issues(t *testing.T, ae *apperr.Error) []string {
	t.Helper()
	raw, err := json.Marshal(ae.Details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var details []struct {
		Params struct {
			Issue struct {
				Path    []any  `json:"path"`
				Message string `json:"message"`
			} `json:"issue"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatalf("unmarshal details %s: %v", raw, err)
	}
	var out []string
	for _, d := range details {
		out = append(out, fmt.Sprint(d.Params.Issue.Path)+": "+d.Params.Issue.Message)
	}
	return out
}

func str(s string) *string { return &s }

func TestCreate(t *testing.T) {
	e := newEnv()
	v, err := e.svc.Create(t.Context(), app.CreateInput{
		BaseURL: "https://GitLab.Corp.Example:443/gitlab/",
		Token:   app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	c := v.Connection
	if c.Name != "gitlab.corp.example" || c.Host != "gitlab.corp.example" || c.BaseURL != "https://gitlab.corp.example/gitlab" {
		t.Errorf("Create() = name %q host %q base %q, want the normalized forms of AC1", c.Name, c.Host, c.BaseURL)
	}
	if !v.TokenConfigured || v.RepoCount != 0 {
		t.Errorf("Create() = token_configured %v repo_count %d, want true, 0", v.TokenConfigured, v.RepoCount)
	}
	if got := e.secrets.GitLab(c.ID); got != "glpat-abcdefghijklmnop" {
		t.Errorf("stored token = %q, want the one sent", got)
	}
	if got := e.gl.ops(); len(got) != 0 {
		t.Errorf("GitLab calls = %v, want none: create makes no request (FR3)", got)
	}
	if !c.CreatedAt.Equal(e.now) || !c.UpdatedAt.Equal(e.now) {
		t.Errorf("timestamps = %v / %v, want %v", c.CreatedAt, c.UpdatedAt, e.now)
	}

	// A second host key on the same host with another port is another connection.
	v2, err := e.svc.Create(t.Context(), app.CreateInput{Name: "  Corp 8443 ", BaseURL: "https://gitlab.corp.example:8443"})
	if err != nil {
		t.Fatalf("Create(port 8443) error = %v", err)
	}
	if v2.Connection.Host != "gitlab.corp.example:8443" || v2.Connection.Name != "Corp 8443" || v2.TokenConfigured {
		t.Errorf("Create(port 8443) = %+v, want host with port, trimmed name, no token", v2)
	}
	if c.ID == v2.Connection.ID {
		t.Errorf("both connections have id %q, want distinct ids", c.ID)
	}
}

func TestCreate_NoToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   app.TokenOp
	}{{"omitted", app.TokenKeep}, {"null", app.TokenRemove}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			v, err := e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.com", Token: app.TokenInput{Op: tc.op}})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if v.TokenConfigured || e.secrets.GitLab(v.Connection.ID) != "" {
				t.Errorf("Create() stored a token, want none")
			}
		})
	}
}

func TestCreate_Validation(t *testing.T) {
	long := strings.Repeat("a", 81)
	tests := []struct {
		name string
		in   app.CreateInput
		want []string
	}{
		{"http base", app.CreateInput{BaseURL: "http://gitlab.example"}, []string{"[base_url]: Base URL must start with https://"}},
		{"api url", app.CreateInput{BaseURL: "https://gitlab.example/api/v4"}, []string{"[base_url]: " + domain.MsgAPIURL}},
		{"github", app.CreateInput{BaseURL: "https://github.com"}, []string{"[base_url]: Base URL must be a GitLab instance, not GitHub"}},
		{"empty base", app.CreateInput{}, []string{"[base_url]: Base URL is required"}},
		{"long name", app.CreateInput{Name: long, BaseURL: "https://gitlab.com"}, []string{"[name]: Name must be at most 80 characters"}},
		{"token with space", app.CreateInput{BaseURL: "https://gitlab.com", Token: app.TokenInput{Op: app.TokenReplace, Value: "a b"}},
			[]string{"[token]: Token must not contain whitespace or control characters"}},
		{"empty token string", app.CreateInput{BaseURL: "https://gitlab.com", Token: app.TokenInput{Op: app.TokenReplace}},
			[]string{"[token]: Token must not be empty"}},
		{"all three", app.CreateInput{Name: long, BaseURL: "http://x", Token: app.TokenInput{Op: app.TokenReplace, Value: "a b"}},
			[]string{
				"[base_url]: Base URL must start with https://",
				"[name]: Name must be at most 80 characters",
				"[token]: Token must not contain whitespace or control characters",
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			_, err := e.svc.Create(t.Context(), tc.in)
			ae := wantErr(t, err, apperr.KindValidation, "validation_error")
			if diff := cmp.Diff(tc.want, issues(t, ae)); diff != "" {
				t.Errorf("Create(%+v) issues mismatch (-want +got):\n%s", tc.in, diff)
			}
			if e.store.count() != 0 || len(e.secrets.tokens) != 0 {
				t.Errorf("Create(%+v) stored something, want nothing (AC2)", tc.in)
			}
		})
	}
}

func TestCreate_ValidationDoesNotEchoTheToken(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.com",
		Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-secret value"}})
	ae := wantErr(t, err, apperr.KindValidation, "validation_error")
	if raw, _ := json.Marshal(ae.Details); strings.Contains(string(raw), "glpat-secret") {
		t.Errorf("validation details = %s, want no token", raw)
	}
}

func TestCreate_ExistsAndLimit(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.corp.example", "gitlab.corp.example", "")
	_, err := e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.corp.example/other",
		Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"}})
	ae := wantErr(t, err, apperr.KindConflict, "gitlab_connection_exists")
	if want := "A GitLab connection for gitlab.corp.example already exists"; ae.Message != want {
		t.Errorf("message = %q, want %q", ae.Message, want)
	}
	if e.store.count() != 1 || len(e.secrets.tokens) != 0 {
		t.Errorf("a refused create left %d connections and %d tokens, want 1 and 0", e.store.count(), len(e.secrets.tokens))
	}

	e = newEnv()
	for i := range domain.Limit {
		e.seed(fmt.Sprintf("%08d-0000-4000-8000-000000000000", i), "c", "https://h", fmt.Sprintf("h%d.example", i), "")
	}
	_, err = e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://new.example"})
	ae = wantErr(t, err, apperr.KindValidation, "gitlab_connection_limit")
	if want := "At most 20 GitLab connections"; ae.Message != want {
		t.Errorf("message = %q, want %q", ae.Message, want)
	}
}

func TestCreate_Concurrent(t *testing.T) {
	e := newEnv()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = e.svc.Create(context.Background(), app.CreateInput{BaseURL: "https://new.example",
				Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"}})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		default:
			wantErr(t, err, apperr.KindConflict, "gitlab_connection_exists")
		}
	}
	if ok != 1 || e.store.count() != 1 || len(e.secrets.tokens) != 1 {
		t.Errorf("8 concurrent creates gave %d successes, %d connections, %d tokens, want 1 each (AC3)", ok, e.store.count(), len(e.secrets.tokens))
	}
}

func TestCreate_SecretsWriteFails(t *testing.T) {
	e := newEnv()
	e.secrets.setErr = errBoom
	_, err := e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.com",
		Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Create() error = %v, want it to wrap the secrets failure (a 500)", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("Create() error = %v, want a plain error (500), not an application error", err)
	}
	if e.store.count() != 0 {
		t.Errorf("connections after a failed token write = %d, want 0 (EC26)", e.store.count())
	}
}

func TestCreate_CommitFailsRestoresToken(t *testing.T) {
	e := newEnv()
	e.store.commitErr = errBoom
	_, err := e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.com",
		Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Create() error = %v, want the commit failure", err)
	}
	if e.store.count() != 0 || len(e.secrets.tokens) != 0 || e.secrets.restores != 1 {
		t.Errorf("after a failed commit: %d connections, %d tokens, %d restores; want 0, 0, 1 (P7)",
			e.store.count(), len(e.secrets.tokens), e.secrets.restores)
	}
}

func TestCreate_StoreRefusalWritesNoToken(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.com", "gitlab.com", "")
	_, _ = e.svc.Create(t.Context(), app.CreateInput{BaseURL: "https://gitlab.com",
		Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-abcdefghijklmnop"}})
	if e.secrets.restores != 0 {
		t.Errorf("restores = %d, want 0: the token is written after the insert, so a refused insert has nothing to restore", e.secrets.restores)
	}
}

func TestUpdate(t *testing.T) {
	setup := func() *env {
		e := newEnv()
		e.seed(id1, "Corp", "https://gitlab.corp.example", "gitlab.corp.example", "glpat-oldoldoldoldold")
		e.repos.counts["gitlab.corp.example"] = 2
		e.now = e.now.Add(time24h)
		return e
	}
	tests := []struct {
		name      string
		in        app.UpdateInput
		wantName  string
		wantBase  string
		wantToken string
		wantCfg   bool
	}{
		{"path prefix only keeps the token", app.UpdateInput{BaseURL: str("https://GitLab.Corp.Example/gitlab/")},
			"Corp", "https://gitlab.corp.example/gitlab", "glpat-oldoldoldoldold", true},
		{"token null removes", app.UpdateInput{Token: app.TokenInput{Op: app.TokenRemove}},
			"Corp", "https://gitlab.corp.example", "", false},
		{"token string replaces", app.UpdateInput{Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-newnewnewnewnew"}},
			"Corp", "https://gitlab.corp.example", "glpat-newnewnewnewnew", true},
		{"name changes", app.UpdateInput{Name: str("  Renamed  ")},
			"Renamed", "https://gitlab.corp.example", "glpat-oldoldoldoldold", true},
		{"empty name becomes the host key", app.UpdateInput{Name: str("   ")},
			"gitlab.corp.example", "https://gitlab.corp.example", "glpat-oldoldoldoldold", true},
		{"nothing", app.UpdateInput{},
			"Corp", "https://gitlab.corp.example", "glpat-oldoldoldoldold", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := setup()
			v, err := e.svc.Update(t.Context(), id1, tc.in)
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			c := v.Connection
			if c.Name != tc.wantName || c.BaseURL != tc.wantBase || c.Host != "gitlab.corp.example" || c.ID != id1 {
				t.Errorf("Update() = %+v, want name %q base %q", c, tc.wantName, tc.wantBase)
			}
			if v.TokenConfigured != tc.wantCfg || e.secrets.GitLab(id1) != tc.wantToken {
				t.Errorf("token_configured %v stored %q, want %v %q", v.TokenConfigured, e.secrets.GitLab(id1), tc.wantCfg, tc.wantToken)
			}
			if v.RepoCount != 2 {
				t.Errorf("repo_count = %d, want 2", v.RepoCount)
			}
			if !c.CreatedAt.Equal(e.now.Add(-time24h)) || !c.UpdatedAt.Equal(e.now) {
				t.Errorf("created %v updated %v, want created kept and updated = now", c.CreatedAt, c.UpdatedAt)
			}
			got, _ := e.store.Get(t.Context(), id1)
			if got != c {
				t.Errorf("stored = %+v, want the returned %+v", got, c)
			}
			if len(e.gl.ops()) != 0 {
				t.Errorf("GitLab calls = %v, want none", e.gl.ops())
			}
		})
	}
}

func TestUpdate_Errors(t *testing.T) {
	e := newEnv()
	before := e.seed(id1, "Corp", "https://gitlab.corp.example", "gitlab.corp.example", "glpat-oldoldoldoldold")

	t.Run("host change", func(t *testing.T) {
		for _, raw := range []string{"https://other.example", "https://gitlab.corp.example:8443", "https://GITLAB.corp.example:8443/x"} {
			_, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Name: str("Changed"), BaseURL: str(raw),
				Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-newnewnewnewnew"}})
			ae := wantErr(t, err, apperr.KindValidation, "validation_error")
			if diff := cmp.Diff([]string{"[base_url]: " + app.MsgHostChange}, issues(t, ae)); diff != "" {
				t.Errorf("Update(%q) issues mismatch (-want +got):\n%s", raw, diff)
			}
		}
		got, _ := e.store.Get(t.Context(), id1)
		if got != before || e.secrets.GitLab(id1) != "glpat-oldoldoldoldold" {
			t.Errorf("a refused update changed the connection (%+v) or its token (%q), want nothing (AC4)", got, e.secrets.GitLab(id1))
		}
	})
	t.Run("invalid fields", func(t *testing.T) {
		_, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Name: str(strings.Repeat("a", 81)), BaseURL: str("https://x.example/api/v4"),
			Token: app.TokenInput{Op: app.TokenReplace, Value: ""}})
		ae := wantErr(t, err, apperr.KindValidation, "validation_error")
		want := []string{
			"[base_url]: " + domain.MsgAPIURL,
			"[token]: Token must not be empty",
			"[name]: Name must be at most 80 characters",
		}
		if diff := cmp.Diff(want, issues(t, ae)); diff != "" {
			t.Errorf("issues mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("unknown and malformed ids", func(t *testing.T) {
		e.seed(idHex, "Hex", "https://hex.example", "hex.example", "")
		for _, id := range []string{id2, "nope", "", "../x", strings.ToUpper(idHex)} {
			_, err := e.svc.Update(t.Context(), id, app.UpdateInput{})
			wantErr(t, err, apperr.KindNotFound, "gitlab_connection_not_found")
		}
	})
	t.Run("unknown id with an invalid body is a validation error first", func(t *testing.T) {
		_, err := e.svc.Update(t.Context(), id2, app.UpdateInput{BaseURL: str("http://x")})
		wantErr(t, err, apperr.KindValidation, "validation_error")
	})
	t.Run("secrets write fails, nothing changes", func(t *testing.T) {
		e.secrets.setErr = errBoom
		defer func() { e.secrets.setErr = nil }()
		_, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Name: str("Changed"),
			Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-newnewnewnewnew"}})
		if !errors.Is(err, errBoom) {
			t.Fatalf("Update() error = %v, want the secrets failure", err)
		}
		got, _ := e.store.Get(t.Context(), id1)
		if got != before || e.secrets.GitLab(id1) != "glpat-oldoldoldoldold" {
			t.Errorf("connection %+v token %q after a failed token write, want both unchanged", got, e.secrets.GitLab(id1))
		}
	})
	t.Run("commit fails, token restored", func(t *testing.T) {
		e.store.commitErr = errBoom
		defer func() { e.store.commitErr = nil }()
		_, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-newnewnewnewnew"}})
		if !errors.Is(err, errBoom) {
			t.Fatalf("Update() error = %v, want the commit failure", err)
		}
		if e.secrets.GitLab(id1) != "glpat-oldoldoldoldold" {
			t.Errorf("token = %q after a failed commit, want the old one back (P7)", e.secrets.GitLab(id1))
		}
	})
}

func TestUpdate_RemoveWithoutTokenWritesNothing(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.com", "gitlab.com", "")
	e.secrets.deleteErr = errBoom
	if _, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Token: app.TokenInput{Op: app.TokenRemove}}); err != nil {
		t.Errorf("Update(token null) without a stored token: error = %v, want none and no secrets write", err)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.corp.example", "gitlab.corp.example", "glpat-oldoldoldoldold")
	e.seed(id2, "Other", "https://other.example", "other.example", "glpat-otherotherother")
	e.repos.counts["gitlab.corp.example"] = 2
	n, err := e.svc.Delete(t.Context(), id1)
	if err != nil || n != 2 {
		t.Fatalf("Delete() = %d, %v, want 2, nil (AC5)", n, err)
	}
	if _, err := e.store.Get(t.Context(), id1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("connection after Delete: err = %v, want not found", err)
	}
	if e.secrets.GitLab(id1) != "" || e.secrets.GitLab(id2) == "" {
		t.Errorf("tokens after Delete: %q / %q, want the deleted one gone and the other kept", e.secrets.GitLab(id1), e.secrets.GitLab(id2))
	}
	_, err = e.svc.Delete(t.Context(), id1)
	wantErr(t, err, apperr.KindNotFound, "gitlab_connection_not_found")
	_, err = e.svc.Delete(t.Context(), "not-a-uuid")
	wantErr(t, err, apperr.KindNotFound, "gitlab_connection_not_found")
}

func TestDelete_NoToken(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.com", "gitlab.com", "")
	e.secrets.deleteErr = errBoom
	if _, err := e.svc.Delete(t.Context(), id1); err != nil {
		t.Errorf("Delete() without a stored token: error = %v, want none", err)
	}
}

func TestDelete_Failures(t *testing.T) {
	setup := func() (*env, domain.Connection) {
		e := newEnv()
		c := e.seed(id1, "Corp", "https://gitlab.corp.example", "gitlab.corp.example", "glpat-oldoldoldoldold")
		return e, c
	}
	t.Run("secrets delete fails", func(t *testing.T) {
		e, c := setup()
		e.secrets.deleteErr = errBoom
		if _, err := e.svc.Delete(t.Context(), id1); !errors.Is(err, errBoom) {
			t.Fatalf("Delete() error = %v, want the secrets failure", err)
		}
		got, err := e.store.Get(t.Context(), id1)
		if err != nil || got != c || e.secrets.GitLab(id1) == "" {
			t.Errorf("after a failed delete: %+v, %v, token %q; want both kept (AC5)", got, err, e.secrets.GitLab(id1))
		}
	})
	t.Run("commit fails", func(t *testing.T) {
		e, c := setup()
		e.store.commitErr = errBoom
		if _, err := e.svc.Delete(t.Context(), id1); !errors.Is(err, errBoom) {
			t.Fatalf("Delete() error = %v, want the commit failure", err)
		}
		got, err := e.store.Get(t.Context(), id1)
		if err != nil || got != c || e.secrets.GitLab(id1) != "glpat-oldoldoldoldold" {
			t.Errorf("after a failed commit: %+v, %v, token %q; want both kept", got, err, e.secrets.GitLab(id1))
		}
	})
	t.Run("count fails", func(t *testing.T) {
		e, _ := setup()
		e.repos.err = errBoom
		if _, err := e.svc.Delete(t.Context(), id1); !errors.Is(err, errBoom) {
			t.Fatalf("Delete() error = %v, want the count failure", err)
		}
		if e.store.count() != 1 || e.secrets.GitLab(id1) == "" {
			t.Errorf("a failed count deleted something")
		}
	})
}

func TestList(t *testing.T) {
	e := newEnv()
	e.seed(id1, "b-corp", "https://b.example:8443", "b.example:8443", "glpat-oldoldoldoldold")
	e.seed(id2, "b-corp", "https://b.example", "b.example", "")
	e.seed("33333333-3333-4333-8333-333333333333", "Alpha", "https://z.example", "z.example", "")
	e.repos.counts["b.example"] = 3
	got, err := e.svc.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var order []string
	for _, v := range got {
		order = append(order, fmt.Sprintf("%s/%s/%v/%d", v.Connection.Name, v.Connection.Host, v.TokenConfigured, v.RepoCount))
	}
	want := []string{"Alpha/z.example/false/0", "b-corp/b.example/false/3", "b-corp/b.example:8443/true/0"}
	if diff := cmp.Diff(want, order); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}

	empty, err := newEnv().svc.List(t.Context())
	if err != nil || len(empty) != 0 {
		t.Errorf("List() on an empty store = %v, %v, want no items", empty, err)
	}
	e.repos.err = errBoom
	if _, err := e.svc.List(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("List() with a failing counter: error = %v, want it wrapped", err)
	}
}

func TestResolveAndAll(t *testing.T) {
	e := newEnv()
	e.seed(id1, "Corp", "https://gitlab.corp.example/gitlab", "gitlab.corp.example", "glpat-oldoldoldoldold")
	e.seed(id2, "Anon", "https://anon.example", "anon.example", "")

	c, ok, err := e.svc.Resolve(t.Context(), "gitlab.corp.example")
	want := app.Conn{ID: id1, Name: "Corp", BaseURL: "https://gitlab.corp.example/gitlab", Host: "gitlab.corp.example", Token: "glpat-oldoldoldoldold"}
	if err != nil || !ok || c != want {
		t.Fatalf("Resolve() = %+v, %v, %v, want %+v", c, ok, err, want)
	}
	if got := c.APIRoot(); got != "https://gitlab.corp.example/gitlab/api/v4" {
		t.Errorf("APIRoot() = %q", got)
	}
	if s := fmt.Sprintf("%v %+v %s", c, c, c); strings.Contains(s, "glpat") {
		t.Errorf("formatted Conn = %q, want no token", s)
	}

	anon, ok, err := e.svc.Resolve(t.Context(), "anon.example")
	if err != nil || !ok || anon.Token != "" {
		t.Errorf("Resolve(anon) = %+v, %v, %v, want found with an empty token", anon, ok, err)
	}
	for _, host := range []string{"missing.example", "GITLAB.corp.example", ""} {
		if _, ok, err := e.svc.Resolve(t.Context(), host); ok || err != nil {
			t.Errorf("Resolve(%q) = ok %v, err %v, want not connected without an error", host, ok, err)
		}
	}

	// A rotated token applies to the next call (FR6).
	if _, err := e.svc.Update(t.Context(), id1, app.UpdateInput{Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-newnewnewnewnew"}}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	c, _, _ = e.svc.Resolve(t.Context(), "gitlab.corp.example")
	if c.Token != "glpat-newnewnewnewnew" {
		t.Errorf("Resolve() token after rotation = %q, want the new one", c.Token)
	}

	all, err := e.svc.All(t.Context())
	if err != nil || len(all) != 2 || all[0].Host != "anon.example" || all[1].Token != "glpat-newnewnewnewnew" {
		t.Errorf("All() = %+v, %v, want the two connections sorted by host with tokens", all, err)
	}
}

func TestResolve_StoreFailure(t *testing.T) {
	e := newEnv()
	e.svc = app.NewService(app.Deps{Store: failingStore{e.store}, Secrets: e.secrets, Repos: e.repos, GitLab: e.gl})
	if _, ok, err := e.svc.Resolve(t.Context(), "gitlab.com"); ok || !errors.Is(err, errBoom) {
		t.Errorf("Resolve() = ok %v, err %v, want a wrapped store failure", ok, err)
	}
	if _, err := e.svc.All(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("All() error = %v, want a wrapped store failure", err)
	}
}

// failingStore fails the reads Resolve and All make.
type failingStore struct{ *fakeStore }

func (failingStore) GetByHost(context.Context, string) (domain.Connection, error) {
	return domain.Connection{}, errBoom
}
func (failingStore) List(context.Context) ([]domain.Connection, error) { return nil, errBoom }

func TestNewServicePanicsWithoutPorts(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("NewService(Deps{}) did not panic, want a wiring panic")
		}
	}()
	app.NewService(app.Deps{})
}
