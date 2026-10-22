package peers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/adapters/peers"
	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/gitcli"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

// heapBudget is NFR5006 / AC5053: the extra heap a generation may hold at any moment.
const heapBudget = 64 << 20

type tourStore struct {
	mu    sync.Mutex
	repo  domain.Repo
	saved []domain.Tour
}

func (s *tourStore) FindRepo(context.Context, string, string) (domain.Repo, error) {
	return s.repo, nil
}

func (s *tourStore) Tour(context.Context, string) (domain.Tour, bool, error) {
	return domain.Tour{}, false, nil
}

func (s *tourStore) SaveTour(_ context.Context, t domain.Tour) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, t)
	return nil
}

type tourIndex struct{ ranked []string }

func (i tourIndex) RepoMap(context.Context, string, int) (string, error) { return "", nil }
func (i tourIndex) TopFilesByRank(_ context.Context, _ string, n int) ([]string, error) {
	return i.ranked[:min(n, len(i.ranked))], nil
}
func (i tourIndex) CriticalPaths(context.Context, string) ([][]string, error) { return nil, nil }
func (i tourIndex) ListIndexedFiles(context.Context, string) ([]string, error) {
	return i.ranked, nil
}

// scriptedLLM answers with an empty tour and keeps the prompt it was given.
type scriptedLLM struct {
	mu     sync.Mutex
	prompt string
}

func (l *scriptedLLM) Structured(_ context.Context, req llm.Request) (llm.Result, error) {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	l.mu.Lock()
	l.prompt = sb.String()
	l.mu.Unlock()
	data := json.RawMessage(`{"architecture":{"summary":"s","nodes":[],"edges":[]},"critical_paths":[],"run_steps":[],"reading_path":[],"first_tasks":[]}`)
	return llm.Result{Data: data, TokensIn: 1, TokensOut: 1, Attempts: 1}, nil
}

type noModels struct{}

func (noModels) Get(context.Context, string) (map[string]json.RawMessage, error) { return nil, nil }

// bigClone makes a real git repository under <root>/acme/shop with README.md of readmeBytes and
// count files src/fNNN.ts of fileBytes each (compressible, so the objects stay small), commits
// it and deletes the worktree files: only the git objects remain, as in a bare mirror.
func bigClone(t *testing.T, readmeBytes, count, fileBytes int, fileMarker string) (root, sha string, ranked []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root = t.TempDir()
	repo := filepath.Join(root, "acme", "shop")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := "documentation line about the shop, nothing secret in here at all\n"
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(strings.Repeat(line, readmeBytes/len(line)+1)[:readmeBytes]), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		path := fmt.Sprintf("src/f%03d.ts", i)
		ranked = append(ranked, path)
		head := fmt.Sprintf("// TODO %s %d\n", fileMarker, i)
		body := strings.Repeat(fmt.Sprintf("export const v%03d = %d;\n", i, i), fileBytes/20+1)
		if err := os.WriteFile(filepath.Join(repo, path), []byte((head + body)[:fileBytes]), 0o644); err != nil {
			t.Fatal(err)
		}
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
	sha = git("rev-parse", "HEAD")
	if err := os.RemoveAll(filepath.Join(repo, "src")); err != nil {
		t.Fatal(err)
	}
	return root, sha, ranked
}

// generate runs one generation over the clone and returns the prompt and the peak extra heap.
func generate(t *testing.T, root, sha string, ranked []string) (prompt string, extraHeap uint64, store *tourStore) {
	t.Helper()
	client, err := gitcli.New(gitcli.Options{CloneRoot: root})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	store = &tourStore{repo: domain.Repo{ID: "repo-1", WorkspaceID: "ws-1", Owner: "acme", Name: "shop", FullName: "acme/shop", DefaultBranch: "main"}}
	model := &scriptedLLM{}
	svc := app.NewService(app.Deps{
		Store:       store,
		IndexStates: peers.NewIndexStates(fakeStates{view: repointel.StateView{FilesIndexed: len(ranked), LastIndexedSha: sha}}),
		Index:       tourIndex{ranked: ranked},
		Blobs:       peers.NewBlobs(client),
		LLM:         model,
		Models:      noModels{},
	})

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				runtime.ReadMemStats(&ms)
				peak = max(peak, ms.HeapAlloc)
			}
		}
	}()
	_, genErr := svc.Generate(t.Context(), "ws-1", "repo-1")
	close(stop)
	<-done
	if genErr != nil {
		t.Fatalf("Generate: %v, want success", genErr)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.prompt, peak - min(peak, base.HeapAlloc), store
}

// TestGenerate_HeapStaysBoundedOverBigClones is AC5053 (NFR5006) over a real git clone read by
// the real gitcli.BlobAt: 200 top-ranked files of 600 KiB (over the 512 KiB cap) and a 40 KiB
// README, a scripted model.
func TestGenerate_HeapStaysBoundedOverBigClones(t *testing.T) {
	readme := func(t *testing.T, prompt string) {
		t.Helper()
		line := "documentation line about the shop, nothing secret in here at all"
		want := strings.Repeat(line+"\n", 40<<10/(len(line)+1)+1)[:40<<10]
		excerpt, ok := domain.ReadmeExcerpt(want)
		if !ok || len(utf16.Encode([]rune(excerpt))) != domain.ReadmeMaxUnits {
			t.Fatalf("ReadmeExcerpt of the 40 KiB README = %d units, %v, want %d", len(utf16.Encode([]rune(excerpt))), ok, domain.ReadmeMaxUnits)
		}
		if !strings.Contains(prompt, excerpt) {
			t.Errorf("the prompt does not contain the README excerpt of %d units", domain.ReadmeMaxUnits)
		}
		if strings.Contains(prompt, excerpt+line[:10]) {
			t.Errorf("the README in the prompt is longer than %d units", domain.ReadmeMaxUnits)
		}
	}

	t.Run("oversize files count as absent", func(t *testing.T) {
		root, sha, ranked := bigClone(t, 40<<10, domain.TopFilesLimit, 600<<10, "oversize-marker")
		prompt, extra, store := generate(t, root, sha, ranked)
		t.Logf("extra heap while generating over 200 x 600 KiB: %d KiB", extra>>10)
		if strings.Contains(prompt, "oversize-marker") {
			t.Error("the prompt carries a TODO line of an oversize file, want those files absent")
		}
		readme(t, prompt)
		if len(store.saved) != 1 {
			t.Errorf("saved tours = %d, want 1", len(store.saved))
		}
		if extra >= heapBudget {
			t.Errorf("extra heap = %d MiB, want under %d MiB", extra>>20, heapBudget>>20)
		}
	})

	// The same count just under the cap: every blob is read and reduced to its TODO lines at once.
	t.Run("files under the cap are not kept", func(t *testing.T) {
		root, sha, ranked := bigClone(t, 40<<10, domain.TopFilesLimit, domain.MaxBlobBytes-1024, "inside-marker")
		prompt, extra, _ := generate(t, root, sha, ranked)
		t.Logf("extra heap while generating over 200 x 511 KiB: %d KiB", extra>>10)
		if !strings.Contains(prompt, "inside-marker") {
			t.Error("the prompt carries no TODO line of the readable files, want some")
		}
		readme(t, prompt)
		if extra >= heapBudget {
			t.Errorf("extra heap = %d MiB, want under %d MiB", extra>>20, heapBudget>>20)
		}
	})
}
