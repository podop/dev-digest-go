package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

// fakeStore answers the calls a test cares about; any other call panics on the nil embedded
// port, which marks a use case that reached further than expected.
type fakeStore struct {
	store

	repo          Repo
	repoMissing   bool
	lockMissing   bool
	files         []FileInfo
	insertErr     error
	replaceErr    error
	listFilesErr  error
	ownerExists   bool
	insertedPaths []string
}

func (f *fakeStore) FindRepo(context.Context, string, string) (Repo, bool, error) {
	return f.repo, !f.repoMissing, nil
}
func (f *fakeStore) LockRepo(context.Context, string, string) (bool, error) {
	return !f.lockMissing, nil
}
func (f *fakeStore) AgentExists(context.Context, string, string) (bool, error) {
	return f.ownerExists, nil
}
func (f *fakeStore) SkillExists(context.Context, string, string) (bool, error) {
	return f.ownerExists, nil
}
func (f *fakeStore) ListFiles(context.Context, string) ([]FileInfo, error) {
	return f.files, f.listFilesErr
}
func (f *fakeStore) InsertFile(_ context.Context, _ string, path, content string, size int) (File, error) {
	f.insertedPaths = append(f.insertedPaths, path)
	if f.insertErr != nil {
		return File{}, f.insertErr
	}
	return File{Path: path, Content: content, SizeBytes: size, Version: 1}, nil
}
func (f *fakeStore) ListUsage(context.Context, string) ([]UsageRow, error) { return nil, nil }
func (f *fakeStore) ReplaceAgentPaths(context.Context, string, string, []string) error {
	return f.replaceErr
}
func (f *fakeStore) ReplaceSkillPaths(context.Context, string, string, []string) error {
	return f.replaceErr
}

// fakeClone has a file at every path when everything is true.
type fakeClone struct{ everything bool }

func (c fakeClone) List(context.Context, string, *domain.GlobSet, int) (ClonedList, bool, error) {
	return ClonedList{}, false, nil
}
func (c fakeClone) Read(string, string) ClonedRead { return ClonedRead{Status: ClonedNotFound} }
func (c fakeClone) Exists(string, string) bool     { return c.everything }

type directTx struct{}

func (directTx) InTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func newService(t *testing.T, st *fakeStore, clone fakeClone) *Service {
	t.Helper()
	svc, err := NewService(st, clone, directTx{}, []string{domain.DefaultGlobs[0]}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func wantApperr(t *testing.T, err error, kind apperr.Kind, code string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != kind || ae.Code != code {
		t.Errorf("error = %v, want %v with code %q", err, kind, code)
	}
}

func TestCreateFile_SuffixExhausted(t *testing.T) {
	clonePath := "/clone"
	st := &fakeStore{repo: Repo{ID: "r", ClonePath: &clonePath}}
	svc := newService(t, st, fakeClone{everything: true})

	_, err := svc.CreateFile(t.Context(), "ws", "r", CreateInput{})
	wantApperr(t, err, apperr.KindConflict, "path_exists")
	if len(st.insertedPaths) != 0 {
		t.Errorf("inserted %v, want nothing", st.insertedPaths)
	}
}

func TestCreateFile_Gone(t *testing.T) {
	tests := []struct {
		name string
		st   *fakeStore
		code string
		kind apperr.Kind
	}{
		{"repo missing", &fakeStore{repoMissing: true}, "repo_not_found", apperr.KindNotFound},
		{"repo deleted before the lock", &fakeStore{lockMissing: true}, "repo_not_found", apperr.KindNotFound},
		{"repo deleted before the insert", &fakeStore{insertErr: ErrRepoGone}, "repo_not_found", apperr.KindNotFound},
		{"path taken by a concurrent writer", &fakeStore{insertErr: ErrPathExists}, "path_exists", apperr.KindConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newService(t, tc.st, fakeClone{}).CreateFile(t.Context(), "ws", "r", CreateInput{})
			wantApperr(t, err, tc.kind, tc.code)
		})
	}
}

func TestCreateFile_StoreFailureIsInternal(t *testing.T) {
	boom := errors.New("connection reset")
	_, err := newService(t, &fakeStore{listFilesErr: boom}, fakeClone{}).CreateFile(t.Context(), "ws", "r", CreateInput{})
	if _, isApp := apperr.As(err); isApp || !errors.Is(err, boom) {
		t.Errorf("CreateFile() error = %v, want the wrapped store error and no application error", err)
	}
}

func TestPutContext_OwnerGone(t *testing.T) {
	st := &fakeStore{ownerExists: true, replaceErr: ErrOwnerGone}
	svc := newService(t, st, fakeClone{})

	_, err := svc.PutAgentContext(t.Context(), "ws", "a", "r", []string{"docs/a.md"})
	wantApperr(t, err, apperr.KindNotFound, "agent_not_found")
	_, err = svc.PutSkillContext(t.Context(), "ws", "s", "r", []string{"docs/a.md"})
	wantApperr(t, err, apperr.KindNotFound, "skill_not_found")
}

func TestPutContext_ChecksOwnerThenRepoThenPaths(t *testing.T) {
	bad := []string{"../a.md"}
	tests := []struct {
		name string
		st   *fakeStore
		code string
	}{
		{"owner first", &fakeStore{repoMissing: true}, "agent_not_found"},
		{"then repo", &fakeStore{ownerExists: true, repoMissing: true}, "repo_not_found"},
		{"then paths", &fakeStore{ownerExists: true}, "invalid_path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newService(t, tc.st, fakeClone{}).PutAgentContext(t.Context(), "ws", "a", "r", bad)
			if ae, ok := apperr.As(err); !ok || ae.Code != tc.code {
				t.Errorf("PutAgentContext() error = %v, want code %q", err, tc.code)
			}
		})
	}
}
