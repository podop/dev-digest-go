package gitlab_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	cigitlab "dev-digest-go/backend/internal/ci/adapters/gitlab"
	"dev-digest-go/backend/internal/ci/app"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

func fixedResolve(b glclient.Binding, ok bool, err error) glclient.ResolveFunc {
	return func(context.Context, string) (glclient.Binding, bool, error) { return b, ok, err }
}

func TestExportConns_Resolve(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name    string
		resolve glclient.ResolveFunc
		want    app.GitLabConn
		found   bool
		wantErr error
	}{
		{name: "connected", resolve: fixedResolve(glclient.Binding{Name: "Corp", BaseURL: "https://gl", Token: testToken}, true, nil),
			want: app.GitLabConn{Name: "Corp", HasToken: true}, found: true},
		{name: "blank token", resolve: fixedResolve(glclient.Binding{Name: "Anon", Token: "  "}, true, nil),
			want: app.GitLabConn{Name: "Anon"}, found: true},
		{name: "no connection", resolve: fixedResolve(glclient.Binding{}, false, nil)},
		{name: "read failure", resolve: fixedResolve(glclient.Binding{}, false, boom), wantErr: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := cigitlab.NewExportConns(tc.resolve).Resolve(t.Context(), "h")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve() error = %v, want %v", err, tc.wantErr)
			}
			if ok != tc.found {
				t.Errorf("Resolve() found = %v, want %v", ok, tc.found)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIngestConns_Resolve(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name    string
		resolve glclient.ResolveFunc
		want    app.GitLabIngestConn
		wantErr error
	}{
		{name: "connected", resolve: fixedResolve(glclient.Binding{BaseURL: "https://gl/prefix", Token: testToken}, true, nil),
			want: app.GitLabIngestConn{BaseURL: "https://gl/prefix"}},
		{name: "no connection", resolve: fixedResolve(glclient.Binding{}, false, nil), wantErr: app.ErrGitLabHostNotConnected},
		{name: "no token", resolve: fixedResolve(glclient.Binding{BaseURL: "https://gl", Token: ""}, true, nil), wantErr: app.ErrGitLabTokenMissing},
		{name: "read failure", resolve: fixedResolve(glclient.Binding{}, false, boom), wantErr: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cigitlab.NewIngestConns(tc.resolve).Resolve(t.Context(), "h")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve() error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Resolve() = %+v, want %+v", got, tc.want)
			}
			if tc.wantErr == app.ErrGitLabHostNotConnected && errors.Is(err, app.ErrGitLabTokenMissing) {
				t.Error("a missing connection reads as a missing token")
			}
		})
	}
}

func TestConstructors_PanicOnNil(t *testing.T) {
	tests := map[string]func(){
		"export conns": func() { cigitlab.NewExportConns(nil) },
		"ingest conns": func() { cigitlab.NewIngestConns(nil) },
		"publisher":    func() { cigitlab.NewPublisher(nil, fixedResolve(glclient.Binding{}, false, nil)) },
		"ci":           func() { cigitlab.NewCI(nil, fixedResolve(glclient.Binding{}, false, nil), nil) },
	}
	for name, f := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("constructor accepted a nil dependency")
				}
			}()
			f()
		})
	}
}
