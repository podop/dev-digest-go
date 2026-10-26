package peers_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/onboarding/adapters/peers"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitcli"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

type fakeStates struct{ view repointel.StateView }

func (f fakeStates) IndexState(context.Context, string) repointel.StateView { return f.view }

func TestIndexStates_MapsFilesAndCommit(t *testing.T) {
	a := peers.NewIndexStates(fakeStates{view: repointel.StateView{FilesIndexed: 42, LastIndexedSha: "abc123"}})
	got := a.IndexState(t.Context(), "repo-1")
	if diff := cmp.Diff(domain.IndexState{FilesIndexed: 42, LastIndexedSHA: "abc123"}, got); diff != "" {
		t.Errorf("IndexState mismatch (-want +got):\n%s", diff)
	}
	if !got.Ready() {
		t.Errorf("IndexState(%+v).Ready() = false, want true", got)
	}
	degraded := peers.NewIndexStates(fakeStates{view: repointel.Synthesised("repo-1")}).IndexState(t.Context(), "repo-1")
	if degraded.Ready() {
		t.Errorf("synthesised state %+v is Ready, want not ready", degraded)
	}
}

type fakeReader struct {
	mapResult repointel.RepoMapResult
	mapErr    error
	askedN    int
	askedExcl []string
}

func (f *fakeReader) RepoMap(context.Context, string, int) (repointel.RepoMapResult, error) {
	return f.mapResult, f.mapErr
}

func (f *fakeReader) TopFilesByRank(_ context.Context, _ string, n int, exclude []string) ([]string, error) {
	f.askedN, f.askedExcl = n, exclude
	return []string{"a.ts"}, nil
}

func (f *fakeReader) CriticalPaths(context.Context, string) ([][]string, error) {
	return [][]string{{"a.ts", "b.ts"}}, nil
}

func (f *fakeReader) ListIndexedFiles(context.Context, string) ([]string, error) {
	return []string{"a.ts", "b.ts"}, nil
}

func TestIndex_RepoMapIsEmptyWhenDegraded(t *testing.T) {
	tests := []struct {
		name string
		res  repointel.RepoMapResult
		want string
	}{
		{"cached", repointel.RepoMapResult{Text: "map", Cached: true}, "map"},
		{"degraded", repointel.RepoMapResult{Degraded: true, Reason: repointel.ReasonNoData}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := peers.NewIndex(&fakeReader{mapResult: tc.res}).RepoMap(t.Context(), "r", 1500)
			if err != nil || got != tc.want {
				t.Errorf("RepoMap = %q, %v, want %q, nil", got, err, tc.want)
			}
		})
	}
	wantErr := errors.New("db down")
	if _, err := peers.NewIndex(&fakeReader{mapErr: wantErr}).RepoMap(t.Context(), "r", 1500); !errors.Is(err, wantErr) {
		t.Errorf("RepoMap error = %v, want %v", err, wantErr)
	}
}

func TestIndex_ForwardsTheReads(t *testing.T) {
	f := &fakeReader{}
	idx := peers.NewIndex(f)
	if got, err := idx.TopFilesByRank(t.Context(), "r", 200); err != nil || len(got) != 1 {
		t.Fatalf("TopFilesByRank = %v, %v", got, err)
	}
	if f.askedN != 200 || f.askedExcl != nil {
		t.Errorf("reader asked for n=%d exclude=%v, want 200 and no extra exclusions", f.askedN, f.askedExcl)
	}
	if got, err := idx.CriticalPaths(t.Context(), "r"); err != nil || len(got) != 1 {
		t.Errorf("CriticalPaths = %v, %v", got, err)
	}
	if got, err := idx.ListIndexedFiles(t.Context(), "r"); err != nil || len(got) != 2 {
		t.Errorf("ListIndexedFiles = %v, %v", got, err)
	}
}

type fakeGit struct {
	data []byte
	err  error
}

func (f fakeGit) BlobAt(context.Context, string, string, string, string, int64) ([]byte, error) {
	return f.data, f.err
}

func TestBlobs_ReadDecodesTheCloneBytes(t *testing.T) {
	tests := []struct {
		name   string
		git    fakeGit
		want   string
		wantOK bool
	}{
		{"plain", fakeGit{data: []byte("hello")}, "hello", true},
		{"empty file exists", fakeGit{data: []byte{}}, "", true},
		{"invalid bytes: one U+FFFD per maximal subpart", fakeGit{data: []byte("a\xe2\x82b")}, "a�b", true},
		{"missing", fakeGit{err: gitcli.ErrBlobMissing}, "", false},
		{"too large", fakeGit{err: gitcli.ErrBlobTooLarge}, "", false},
		{"git failing", fakeGit{err: errors.New("fatal: boom")}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := peers.NewBlobs(tc.git).Read(t.Context(), "o", "r", "abc", "x", 10)
			if ok != tc.wantOK || string(got) != tc.want {
				t.Errorf("Read = %q, %v, want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestBlobs_ReadFromARealClone reads blobs of a real repository at its commit: a present file, a
// missing one, one over the cap, and an unsafe path.
func TestBlobs_ReadFromARealClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "acme", "shop")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Shop\n")
	write("big.txt", strings.Repeat("x", 600))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	sha := git("rev-parse", "HEAD")

	client, err := gitcli.New(gitcli.Options{CloneRoot: root})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	b := peers.NewBlobs(client)
	tests := []struct {
		name, path string
		limit      int64
		want       string
		wantOK     bool
	}{
		{"present", "README.md", 500, "# Shop\n", true},
		{"missing", "compose.yml", 500, "", false},
		{"over the cap", "big.txt", 500, "", false},
		{"unsafe path", "../outside", 500, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := b.Read(t.Context(), "acme", "shop", sha, tc.path, tc.limit)
			if ok != tc.wantOK || string(got) != tc.want {
				t.Errorf("Read(%q) = %q, %v, want %q, %v", tc.path, got, ok, tc.want, tc.wantOK)
			}
		})
	}
	if _, ok := b.Read(t.Context(), "acme", "shop", "not-a-sha", "README.md", 500); ok {
		t.Error("Read at a non-hex commit = ok, want absent")
	}
}

// TestBlobs_ReadFromAGitLabClone reads a blob through the clone key the store puts in
// Repo.Owner/Repo.Name for a GitLab repository, from the directory codehost.CloneKey names.
func TestBlobs_ReadFromAGitLabClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	first, second := codehost.CloneKey(codehost.GitLab, "git.corp.example:8443", 4242, "grp/sub", "web")
	root := t.TempDir()
	repo := filepath.Join(root, filepath.FromSlash(first), second)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	sha := git("rev-parse", "HEAD")

	client, err := gitcli.New(gitcli.Options{CloneRoot: root})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	b := peers.NewBlobs(client)
	if got, ok := b.Read(t.Context(), first, second, sha, "README.md", 500); !ok || string(got) != "# Web\n" {
		t.Errorf("Read(%q, %q) = %q, %v, want the file", first, second, got, ok)
	}
	// The project path is not a clone location: GitHub-style (owner, name) finds nothing.
	if _, ok := b.Read(t.Context(), "grp/sub", "web", sha, "README.md", 500); ok {
		t.Error("Read(project path) = ok, want absent")
	}
}

// TestBlobs_ReadEqualsDecodeText pins the fast path for valid UTF-8 to the decoder the rest of the
// backend uses on clone bytes.
func TestBlobs_ReadEqualsDecodeText(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := []byte{'a', 'Z', 0, '\n', 0xC2, 0xA9, 0xE2, 0x82, 0xAC, 0xF0, 0x9F, 0x98, 0x80, 0xED, 0xA0, 0x80, 0xFF, 0xEF, 0xBB, 0xBF}
	for range 5000 {
		data := make([]byte, rng.IntN(12))
		for i := range data {
			data[i] = alphabet[rng.IntN(len(alphabet))]
		}
		got, ok := peers.NewBlobs(fakeGit{data: data}).Read(t.Context(), "o", "r", "abc", "x", 100)
		if want := repointel.DecodeText(data); !ok || string(got) != want {
			t.Fatalf("Read(%q) = %q, %v, want %q", data, got, ok, want)
		}
	}
}
