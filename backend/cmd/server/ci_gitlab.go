package main

import (
	"net/http"

	cigl "dev-digest-go/backend/internal/ci/adapters/gitlab"
	cipeers "dev-digest-go/backend/internal/ci/adapters/peers"
	ciapp "dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// ciGitLab holds the GitLab CI adapters (GitLab C, FR31): they read the same connection as the
// other GitLab contexts (glResolve, read at every call, so a changed or removed token applies to
// the next request) and talk through the same egress client (no proxy, no redirects, guarded
// addresses). The export and the ingest take their share through export and ingest.
type ciGitLab struct {
	conns     *cigl.ExportConns
	clones    cipeers.CloneFiles
	publisher *cigl.Publisher
	ingest    *cigl.IngestConns
	reader    *cigl.CI
}

// newCIGitLab builds the adapters over the GitLab client and the binding lookup. storage is the
// client of the artifact object-storage hop; nil = the GitLab client's own egress client. The
// platform client sends it no credential and follows no redirect with it.
func newCIGitLab(client *gitlab.Client, resolve gitlab.ResolveFunc, storage *http.Client) ciGitLab {
	return ciGitLab{
		conns:     cigl.NewExportConns(resolve),
		clones:    cipeers.NewCloneFiles(),
		publisher: cigl.NewPublisher(client, resolve),
		ingest:    cigl.NewIngestConns(resolve),
		reader:    cigl.NewCI(client, resolve, storage),
	}
}

// export sets the GitLab ports of the export (a gitlab_ci export, FR1-FR14). RetryPause stays at
// its default (500 ms between the two attempts of a publish sequence).
func (g ciGitLab) export(d ciapp.ExportDeps) ciapp.ExportDeps {
	d.GitLabConns = g.conns
	d.Clones = g.clones
	d.GitLab = g.publisher
	return d
}

// ingestDeps returns the GitLab ports of the refresh (FR31-FR36): the store is the CI store (host-
// qualified runs) and the ledger the cost ledger adapter of this root.
func (g ciGitLab) ingestDeps(store ciapp.GitLabIngestStore, ledger ciapp.GitLabLedger) ciapp.GitLabIngestDeps {
	return ciapp.GitLabIngestDeps{Conns: g.ingest, CI: g.reader, Store: store, Ledger: ledger}
}
