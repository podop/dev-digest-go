package onboardinghttp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	onboardinghttp "dev-digest-go/backend/internal/onboarding/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
)

// The Node route goldens (parity/testdata/goldens/routes.json, group onboarding) are replayed
// against the real use cases over in-memory ports and the real routes: status and body text,
// key order included. The world is the generate_ok flow of goldens/onboarding.json.

const (
	routesGolden     = "../../../parity/testdata/goldens/routes.json"
	onboardingGolden = "../../../parity/testdata/goldens/onboarding.json"
)

type routeStep struct {
	Request *struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	} `json:"request"`
	Status int    `json:"status"`
	Body   string `json:"body"`
	World  *struct {
		Index *struct {
			FilesIndexed   int    `json:"filesIndexed"`
			LastIndexedSha string `json:"lastIndexedSha"`
		} `json:"index"`
		LLM *struct {
			Error string `json:"error"`
			Mock  bool   `json:"mock"`
		} `json:"llm"`
	} `json:"world"`
}

type goldenWorld struct {
	Ranked  []string          `json:"ranked"`
	Chains  [][]string        `json:"chains"`
	Indexed []string          `json:"indexed"`
	Blobs   map[string]string `json:"blobs"`
	RepoMap struct {
		Text string `json:"text"`
	} `json:"repoMap"`
	LLM struct {
		Data      json.RawMessage `json:"data"`
		TokensIn  int             `json:"tokensIn"`
		TokensOut int             `json:"tokensOut"`
		CostUsd   *float64        `json:"costUsd"`
	} `json:"llm"`
}

type memStore struct {
	mu   sync.Mutex
	repo domain.Repo
	tour *domain.Tour
}

func (s *memStore) FindRepo(_ context.Context, ws, id string) (domain.Repo, error) {
	if ws != s.repo.WorkspaceID || id != s.repo.ID {
		return domain.Repo{}, domain.ErrNotFound
	}
	return s.repo, nil
}

func (s *memStore) Tour(context.Context, string) (domain.Tour, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tour == nil {
		return domain.Tour{}, false, nil
	}
	return *s.tour, true, nil
}

func (s *memStore) SaveTour(_ context.Context, t domain.Tour) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tour = &t
	return nil
}

type mutableIndexState struct {
	mu    sync.Mutex
	state domain.IndexState
}

func (m *mutableIndexState) IndexState(context.Context, string) domain.IndexState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *mutableIndexState) set(s domain.IndexState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = s
}

type memIndex struct{ w goldenWorld }

func (i memIndex) RepoMap(context.Context, string, int) (string, error) { return i.w.RepoMap.Text, nil }
func (i memIndex) TopFilesByRank(_ context.Context, _ string, n int) ([]string, error) {
	return i.w.Ranked[:min(n, len(i.w.Ranked))], nil
}
func (i memIndex) CriticalPaths(context.Context, string) ([][]string, error) { return i.w.Chains, nil }
func (i memIndex) ListIndexedFiles(context.Context, string) ([]string, error) {
	return i.w.Indexed, nil
}

type memBlobs map[string]string

func (b memBlobs) Read(_ context.Context, _, _, _, path string, _ int64) ([]byte, bool) {
	text, ok := b[path]
	return []byte(text), ok
}

type scriptedLLM struct {
	mu   sync.Mutex
	w    goldenWorld
	mode string // "", "config", "mock"
}

func (l *scriptedLLM) setMode(m string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.mode = m
}

func (l *scriptedLLM) Structured(context.Context, llm.Request) (llm.Result, error) {
	l.mu.Lock()
	mode := l.mode
	l.mu.Unlock()
	switch mode {
	case "config":
		return llm.Result{}, apperr.Config("OPENROUTER_API_KEY is not configured")
	case "mock":
		return llm.Result{}, apperr.ExternalService("Mock LLM provider has no fixture for structured output 'OnboardingTour'")
	}
	return llm.Result{Data: l.w.LLM.Data, TokensIn: l.w.LLM.TokensIn, TokensOut: l.w.LLM.TokensOut, CostUSD: l.w.LLM.CostUsd}, nil
}

type noModels struct{}

func (noModels) Get(context.Context, string) (map[string]json.RawMessage, error) { return nil, nil }

func TestRoutes_MatchTheNodeRouteGolden(t *testing.T) {
	var routes struct {
		IDs    struct{ Repo, OtherRepo string } `json:"ids"`
		Groups struct {
			Onboarding []routeStep `json:"onboarding"`
		} `json:"groups"`
	}
	readJSON(t, routesGolden, &routes)
	var flows struct {
		Flows []struct {
			Name  string      `json:"name"`
			World goldenWorld `json:"world"`
		} `json:"flows"`
	}
	readJSON(t, onboardingGolden, &flows)
	var world goldenWorld
	for _, f := range flows.Flows {
		if f.Name == "generate_ok" {
			world = f.World
		}
	}
	if len(world.Indexed) == 0 || len(routes.Groups.Onboarding) == 0 {
		t.Fatal("goldens are missing the generate_ok world or the onboarding route group")
	}

	store := &memStore{repo: domain.Repo{ID: routes.IDs.Repo, WorkspaceID: workspaceID, Owner: "acme", Name: "shop", FullName: "acme/shop", DefaultBranch: "main"}}
	states := &mutableIndexState{state: domain.IndexState{FilesIndexed: 42, LastIndexedSHA: "abc123def456"}}
	model := &scriptedLLM{w: world}
	svc := app.NewService(app.Deps{
		Store: store, IndexStates: states, Index: memIndex{w: world}, Blobs: memBlobs(world.Blobs), LLM: model, Models: noModels{},
		Now: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
		Log: slog.New(slog.DiscardHandler),
	})
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{uiOrigin}, AllowedHosts: []string{"127.0.0.1"}})
	onboardinghttp.AddRoutes(e, svc, fakeTenants{})
	srv := newServerFor(t, e)

	for i, step := range routes.Groups.Onboarding {
		if step.World != nil {
			if step.World.Index != nil {
				states.set(domain.IndexState{FilesIndexed: step.World.Index.FilesIndexed, LastIndexedSHA: step.World.Index.LastIndexedSha})
			}
			if step.World.LLM != nil {
				switch {
				case step.World.LLM.Error == "config":
					model.setMode("config")
				case step.World.LLM.Mock:
					model.setMode("mock")
				default:
					model.setMode("")
				}
			}
			continue
		}
		got := do(t, srv, step.Request.Method, step.Request.URL, "", "", map[string]string{"Origin": uiOrigin})
		if step.Status == http.StatusUnprocessableEntity && strings.Contains(step.Body, `"validation_error"`) {
			// The path-id 422 is built by platform/httpx (Phase 1): its details carry the same
			// keyword, instancePath and message as Node's but in another key order and without
			// params.issue.validation, which the UI does not read. Compare what the UI reads.
			if got.Status != step.Status || validationSummary(t, got.Body) != validationSummary(t, step.Body) {
				t.Errorf("step %d %s %s = %d %s, want %d like %s", i, step.Request.Method, step.Request.URL, got.Status, got.Body, step.Status, step.Body)
			}
			continue
		}
		if got.Status != step.Status || strings.TrimSuffix(got.Body, "\n") != step.Body {
			t.Errorf("step %d %s %s = %d\n%s\nwant %d\n%s", i, step.Request.Method, step.Request.URL, got.Status, got.Body, step.Status, step.Body)
		}
	}
}

// validationSummary is the code, message and per-issue keyword, path and message of a 422 body.
func validationSummary(t *testing.T, body string) string {
	t.Helper()
	var b struct {
		Error struct {
			Code, Message string
			Details       []struct{ Keyword, InstancePath, Message string }
		}
	}
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
