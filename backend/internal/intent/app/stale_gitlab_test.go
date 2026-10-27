package app_test

import (
	"slices"
	"testing"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
)

const glWeb = "https://git.example.com:8443/acme/core/payments"

// gitlabWorld is a world whose repository is a GitLab project.
func gitlabWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, app.Options{})
	*w.pulls.repo = domain.Repo{
		ID: repoID, Owner: "acme/core", Name: "payments", Provider: "gitlab",
		Host: "git.example.com:8443", ProjectID: 42, WebURL: glWeb,
	}
	return w
}

// A GitLab repository's references are extracted with the GitLab rules (FR26, FR28): the same-project
// issue URL and blob URL are read, the other project's are skipped.
func TestResolve_GitLabRepositoryUsesTheGitLabExtraction(t *testing.T) {
	w := gitlabWorld(t)
	w.pulls.pull.Body = "Closes " + glWeb + "/-/issues/7 and https://git.example.com:8443/acme/other/-/issues/9\n" +
		"See [d](" + glWeb + "/-/blob/main/docs/a.md) and https://git.example.com:8443/acme/other/-/blob/main/docs/b.md"
	w.tickets.reads[7] = app.TicketRead{OK: true, Title: "Issue", Body: "text"}
	w.docs.reads["docs/a.md"] = app.DocRead{OK: true, Content: "# A"}

	if r := w.svc.Resolve(t.Context(), w.input(false), nil); !r.Used {
		t.Fatalf("Resolve() = %+v, want a derivation", r)
	}

	if want := []float64{7}; !slices.Equal(w.tickets.calls, want) {
		t.Errorf("tickets read = %v, want %v (the other project's #9 is not read)", w.tickets.calls, want)
	}
	if want := []string{headSHA + ":docs/a.md"}; !slices.Equal(w.docs.calls, want) {
		t.Errorf("docs read = %v, want %v (the other project's doc is not read)", w.docs.calls, want)
	}
}

// The staleness key follows the GitLab extraction too (FR26): a same-project ticket reference in a
// new commit changes the key (ticket numbers are part of it), a reference to another project, a
// GitHub issue URL or text without a reference does not. Under the GitHub rules the first row would
// not match at all.
func TestGet_StalenessOfAGitLabRepository(t *testing.T) {
	tests := []struct {
		name      string
		commit    string
		wantStale bool
	}{
		{"same-project issue URL", "Closes " + glWeb + "/-/issues/7", true},
		{"same-project work item URL", "Closes " + glWeb + "/-/work_items/7", true},
		{"same-project namespaced reference", "Fixes acme/core/payments#8", true},
		{"bare reference", "Fixes #12", true},
		{"another project's issue URL", "Closes https://git.example.com:8443/acme/other/-/issues/7", false},
		{"another project's reference", "Fixes acme/other#8", false},
		{"another host's issue URL", "Closes https://gitlab.com/acme/core/payments/-/issues/7", false},
		{"GitHub issue URL", "Closes https://github.com/acme/payments/issues/7", false},
		{"no reference", "tidy", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := gitlabWorld(t)
			if r := w.svc.Resolve(t.Context(), w.input(false), nil); !r.Used {
				t.Fatalf("setup derivation = %+v", r)
			}
			w.pulls.commits = []domain.Commit{{Message: tc.commit}}

			got, err := w.svc.Get(t.Context(), wsID, prID)

			if err != nil || got.Intent == nil {
				t.Fatalf("Get() = %+v, %v, want a record", got, err)
			}
			if got.Stale != tc.wantStale {
				t.Errorf("Get(commit %q).Stale = %v, want %v", tc.commit, got.Stale, tc.wantStale)
			}
		})
	}
}
