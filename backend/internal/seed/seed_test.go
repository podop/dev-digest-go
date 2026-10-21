package seed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPR482Patches_MatchNodeGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/pr482_patches.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(want) != 4 {
		t.Fatalf("golden has %d patches, want 4", len(want))
	}
	if diff := cmp.Diff(want, pr482Patches()); diff != "" {
		t.Errorf("pr482Patches() mismatch against Node SEED_PR_482_PATCHES (-want +got):\n%s", diff)
	}
}

// fakeTx records the calls the seed makes and hands out predictable ids.
type fakeTx struct {
	calls   []string
	failOn  string
	patches map[string]string
}

func (f *fakeTx) record(name string) error {
	f.calls = append(f.calls, name)
	if name == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeTx) EnsureWorkspace(_ context.Context, name string) (string, bool, error) {
	return "ws", true, f.record("workspace:" + name)
}
func (f *fakeTx) EnsureUser(_ context.Context, email, _ string) (string, bool, error) {
	return "user", true, f.record("user:" + email)
}
func (f *fakeTx) EnsureMember(_ context.Context, ws, user, role string) (bool, error) {
	return true, f.record("member:" + ws + "/" + user + "/" + role)
}
func (f *fakeTx) EnsureSetting(_ context.Context, _, _, key string, value json.RawMessage) (bool, error) {
	return true, f.record("setting:" + key + "=" + string(value))
}
func (f *fakeTx) EnsureRepo(_ context.Context, r Repo) (string, bool, error) {
	return "repo", true, f.record("repo:" + r.FullName + "@" + r.WorkspaceID)
}
func (f *fakeTx) EnsurePullRequest(_ context.Context, p PullRequest) (string, bool, error) {
	return "pr", true, f.record("pr:" + p.RepoID)
}
func (f *fakeTx) EnsureFile(_ context.Context, prID string, file File) (bool, bool, error) {
	f.patches[file.Path] = file.Patch
	return true, false, f.record("file:" + prID + "/" + file.Path)
}
func (f *fakeTx) EnsureCommit(_ context.Context, prID string, c Commit) (bool, error) {
	return true, f.record("commit:" + prID + "/" + c.SHA)
}

type fakeStore struct{ tx *fakeTx }

func (s fakeStore) InTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, s.tx)
}

func TestSeederRun(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}}
	res, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantCalls := []string{
		"workspace:default", "user:you@local", "member:ws/user/owner",
		"setting:polling_interval_min=5", `setting:theme="dark"`, `setting:density="regular"`, "setting:sync_to_folder=true",
		"repo:acme/payments-api@ws", "pr:repo",
		"file:pr/src/middleware/ratelimit.ts", "file:pr/src/api/public/webhooks.ts",
		"file:pr/src/config.ts", "file:pr/src/api/users.ts",
		"commit:pr/a1b2c3d4e5f6",
	}
	if diff := cmp.Diff(wantCalls, tx.calls); diff != "" {
		t.Errorf("Run() calls mismatch (-want +got):\n%s", diff)
	}
	wantRes := Result{WorkspaceID: "ws", UserID: "user", Workspaces: 1, Users: 1, Members: 1, Settings: 4, Repos: 1, PullRequests: 1, Files: 4, Commits: 1}
	if diff := cmp.Diff(wantRes, res); diff != "" {
		t.Errorf("Run() result mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(pr482Patches(), tx.patches); diff != "" {
		t.Errorf("Run() file patches mismatch (-want +got):\n%s", diff)
	}
}

func TestSeederRun_ErrorNamesTheStep(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}, failOn: "commit:pr/a1b2c3d4e5f6"}
	_, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
	if err == nil || err.Error() != "seed: pr commit: boom" {
		t.Errorf("Run() error = %v, want %q", err, "seed: pr commit: boom")
	}
}
