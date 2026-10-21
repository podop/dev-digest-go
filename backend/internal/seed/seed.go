// Package seed writes the starter data of the Go backend: the default workspace, the system
// user, four user settings and the demo repository acme/payments-api with PR #482, its four
// files and one commit.
//
// The seed is idempotent. Rows are matched by natural key and never overwritten; the only
// update is filling a file patch that is still null. The whole run is one transaction.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"dev-digest-go/backend/internal/tenancy"
)

// Repo is the demo repository row.
type Repo struct {
	WorkspaceID   string
	Owner         string
	Name          string
	FullName      string
	DefaultBranch string
	CreatedBy     string
}

// PullRequest is the demo pull request row. Timestamps and the review sha stay null.
type PullRequest struct {
	WorkspaceID string
	RepoID      string
	Number      int
	Title       string
	Author      string
	Branch      string
	Base        string
	HeadSHA     string
	Additions   int
	Deletions   int
	FilesCount  int
	Status      string
	Body        string
}

// File is one changed file of a pull request.
type File struct {
	Path      string
	Additions int
	Deletions int
	Patch     string
}

// Commit is one commit of a pull request; committed_at stays null.
type Commit struct {
	SHA     string
	Message string
	Author  string
}

// Store runs the seed in a single transaction.
type Store interface {
	// InTx runs fn in one transaction; any error returned by fn rolls everything back.
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx holds the idempotent write operations. Each Ensure method finds the row by its natural
// key and inserts it when missing; the bool reports whether a row was inserted. Existing
// rows are never modified.
type Tx interface {
	EnsureWorkspace(ctx context.Context, name string) (id string, inserted bool, err error)
	EnsureUser(ctx context.Context, email, name string) (id string, inserted bool, err error)
	EnsureMember(ctx context.Context, workspaceID, userID, role string) (inserted bool, err error)
	EnsureSetting(ctx context.Context, workspaceID, userID, key string, value json.RawMessage) (inserted bool, err error)
	EnsureRepo(ctx context.Context, r Repo) (id string, inserted bool, err error)
	EnsurePullRequest(ctx context.Context, p PullRequest) (id string, inserted bool, err error)
	// EnsureFile inserts the file, or, when it exists with a null patch, fills the patch.
	EnsureFile(ctx context.Context, prID string, f File) (inserted, patchFilled bool, err error)
	EnsureCommit(ctx context.Context, prID string, c Commit) (inserted bool, err error)
}

// Result counts the rows a run inserted, per entity.
type Result struct {
	WorkspaceID   string
	UserID        string
	Workspaces    int
	Users         int
	Members       int
	Settings      int
	Repos         int
	PullRequests  int
	Files         int
	Commits       int
	PatchesFilled int
}

// Seeder runs the seed.
type Seeder struct {
	store Store
	log   *slog.Logger
}

// New builds a Seeder.
func New(store Store, log *slog.Logger) *Seeder { return &Seeder{store: store, log: log} }

// defaultSettings are the per-user settings of the system user, in write order.
var defaultSettings = []struct {
	key   string
	value json.RawMessage
}{
	{"polling_interval_min", json.RawMessage(`5`)},
	{"theme", json.RawMessage(`"dark"`)},
	{"density", json.RawMessage(`"regular"`)},
	{"sync_to_folder", json.RawMessage(`true`)},
}

// Run writes the seed data in one transaction and logs one line with the rows inserted per
// entity. On error nothing is written.
func (s *Seeder) Run(ctx context.Context) (Result, error) {
	var res Result
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		res = Result{} // a retried or re-run closure must not double count
		return s.write(ctx, tx, &res)
	})
	if err != nil {
		return Result{}, fmt.Errorf("seed: %w", err)
	}
	s.log.InfoContext(ctx, "seed complete",
		"workspaces", res.Workspaces, "users", res.Users, "members", res.Members,
		"settings", res.Settings, "repos", res.Repos, "pull_requests", res.PullRequests,
		"pr_files", res.Files, "pr_commits", res.Commits, "patches_filled", res.PatchesFilled)
	return res, nil
}

func (s *Seeder) write(ctx context.Context, tx Tx, res *Result) error {
	workspaceID, inserted, err := tx.EnsureWorkspace(ctx, tenancy.DefaultWorkspaceName)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	res.WorkspaceID = workspaceID
	res.Workspaces += b2i(inserted)

	userID, inserted, err := tx.EnsureUser(ctx, tenancy.SystemUserEmail, tenancy.SystemUserName)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	res.UserID = userID
	res.Users += b2i(inserted)

	inserted, err = tx.EnsureMember(ctx, workspaceID, userID, "owner")
	if err != nil {
		return fmt.Errorf("workspace member: %w", err)
	}
	res.Members += b2i(inserted)

	for _, setting := range defaultSettings {
		inserted, err = tx.EnsureSetting(ctx, workspaceID, userID, setting.key, setting.value)
		if err != nil {
			return fmt.Errorf("setting %s: %w", setting.key, err)
		}
		res.Settings += b2i(inserted)
	}

	repoID, inserted, err := tx.EnsureRepo(ctx, Repo{
		WorkspaceID:   workspaceID,
		Owner:         "acme",
		Name:          "payments-api",
		FullName:      "acme/payments-api",
		DefaultBranch: "main",
		CreatedBy:     userID,
	})
	if err != nil {
		return fmt.Errorf("repo: %w", err)
	}
	res.Repos += b2i(inserted)

	prID, inserted, err := tx.EnsurePullRequest(ctx, PullRequest{
		WorkspaceID: workspaceID,
		RepoID:      repoID,
		Number:      482,
		Title:       "Add rate limiting to public API endpoints",
		Author:      "marisa.koch",
		Branch:      "feat/rate-limit-public",
		Base:        "main",
		HeadSHA:     "a1b2c3d4e5f6",
		Additions:   247,
		Deletions:   38,
		FilesCount:  9,
		Status:      "needs_review",
		Body:        "Add rate limiting to public API endpoints to prevent abuse from unauthenticated clients.",
	})
	if err != nil {
		return fmt.Errorf("pull request 482: %w", err)
	}
	res.PullRequests += b2i(inserted)

	patches := pr482Patches()
	for _, f := range []File{
		{Path: "src/middleware/ratelimit.ts", Additions: 84, Deletions: 0},
		{Path: "src/api/public/webhooks.ts", Additions: 31, Deletions: 6},
		{Path: "src/config.ts", Additions: 4, Deletions: 0},
		{Path: "src/api/users.ts", Additions: 7, Deletions: 2},
	} {
		f.Patch = patches[f.Path]
		inserted, filled, err := tx.EnsureFile(ctx, prID, f)
		if err != nil {
			return fmt.Errorf("pr file %s: %w", f.Path, err)
		}
		res.Files += b2i(inserted)
		res.PatchesFilled += b2i(filled)
	}

	inserted, err = tx.EnsureCommit(ctx, prID, Commit{
		SHA:     "a1b2c3d4e5f6",
		Message: "Add token-bucket rate limiter",
		Author:  "marisa.koch",
	})
	if err != nil {
		return fmt.Errorf("pr commit: %w", err)
	}
	res.Commits += b2i(inserted)
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
