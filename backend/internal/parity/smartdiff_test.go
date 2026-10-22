package parity_test

// S30 (AC5043, AC5006): the smart-diff context against testdata/goldens/smartdiff.json — the role
// of every corpus path, the grouped read model, and the whole route over a real Postgres store
// (the "latest review per agent" step is SQL in Node and emulated by the generator).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	smartdiffpg "dev-digest-go/backend/internal/smartdiff/adapters/postgres"
	"dev-digest-go/backend/internal/smartdiff/app"
	"dev-digest-go/backend/internal/smartdiff/domain"
	smartdiffhttp "dev-digest-go/backend/internal/smartdiff/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

func TestSmartDiffGolden_Classify(t *testing.T) {
	var g struct {
		Classify []struct {
			Path string `json:"path"`
			Role string `json:"role"`
		} `json:"classify"`
	}
	loadGolden(t, "smartdiff", &g)
	if len(g.Classify) < 100 {
		t.Fatalf("the golden holds %d classify cases, want the 170-path corpus", len(g.Classify))
	}
	for _, tc := range g.Classify {
		if got := domain.Classify(tc.Path); string(got) != tc.Role {
			t.Errorf("Classify(%q) = %q, want %q", tc.Path, got, tc.Role)
		}
	}
}

type fakeIdentity struct{ workspace string }

func (f fakeIdentity) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: f.workspace}, nil
}

// getSmartDiff serves GET /pulls/<id>/smart-diff through the real route and returns the status
// and the body.
func getSmartDiff(t *testing.T, api app.API, workspace, id string) (int, []byte) {
	t.Helper()
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), AllowedHosts: []string{"127.0.0.1"}})
	smartdiffhttp.AddRoutes(e, api, fakeIdentity{workspace})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Get(srv.URL + "/pulls/" + id + "/smart-diff")
	if err != nil {
		t.Fatalf("GET smart-diff: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, b
}

type buildAPI struct{ result domain.SmartDiff }

func (b buildAPI) Get(context.Context, string, string) (domain.SmartDiff, error) {
	return b.result, nil
}

func TestSmartDiffGolden_Build(t *testing.T) {
	var g struct {
		Build []struct {
			Name  string `json:"name"`
			Files []struct {
				Path      string `json:"path"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"files"`
			Findings []struct {
				File      string `json:"file"`
				StartLine int    `json:"start_line"`
			} `json:"findings"`
			Result json.RawMessage `json:"result"`
		} `json:"build"`
	}
	loadGolden(t, "smartdiff", &g)
	if len(g.Build) == 0 {
		t.Fatal("smartdiff.json has no build cases")
	}
	const anyID = "7e1d0000-0000-4000-8000-000000000001"
	for _, tc := range g.Build {
		t.Run(tc.Name, func(t *testing.T) {
			var files []domain.PullFile
			for _, f := range tc.Files {
				files = append(files, domain.PullFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
			}
			var findings []domain.FindingRef
			for _, f := range tc.Findings {
				findings = append(findings, domain.FindingRef{File: f.File, StartLine: f.StartLine})
			}
			// The route renders the read model in the wire shape of the response.
			status, body := getSmartDiff(t, buildAPI{domain.Build(files, findings)}, "ws", anyID)
			if status != http.StatusOK {
				t.Fatalf("GET smart-diff = %d %s, want 200", status, body)
			}
			if diff := cmp.Diff(jsonValue(t, tc.Result), jsonValue(t, body)); diff != "" {
				t.Errorf("Build(%+v, %+v) response mismatch (-want +got):\n%s", tc.Files, tc.Findings, diff)
			}
		})
	}
}

// AC5006 (store): the golden's tables seeded into a real database, the route's answer equals the
// body Node's service built from them.
func TestSmartDiffGolden_ServiceOverPostgres(t *testing.T) {
	var corpus struct {
		Workspace string `json:"workspace"`
	}
	loadCorpus(t, "smartdiff", "service", &corpus)
	var g struct {
		Service []struct {
			Name   string `json:"name"`
			AskFor string `json:"askFor"`
			Tables struct {
				PR    *struct{ ID string } `json:"pr"`
				Files []struct {
					Path      string `json:"path"`
					Additions int    `json:"additions"`
					Deletions int    `json:"deletions"`
				} `json:"files"`
				Reviews []struct {
					ID        string  `json:"id"`
					AgentID   *string `json:"agent_id"`
					Kind      string  `json:"kind"`
					CreatedAt string  `json:"created_at"`
				} `json:"reviews"`
				Findings []struct {
					ReviewID  string `json:"review_id"`
					File      string `json:"file"`
					StartLine int    `json:"start_line"`
					Dismissed bool   `json:"dismissed"`
				} `json:"findings"`
			} `json:"tables"`
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"service"`
	}
	loadGolden(t, "smartdiff", &g)
	if len(g.Service) == 0 {
		t.Fatal("smartdiff.json has no service cases")
	}

	d := pgtest.Migrated(t)
	db := d.DB
	insert := func(query string, args ...any) string {
		t.Helper()
		var id string
		if err := db.Raw(query, args...).Scan(&id).Error; err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
		return id
	}
	exec := func(db *gorm.DB, query string, args ...any) {
		t.Helper()
		if err := db.Exec(query, args...).Error; err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	ws := insert(`INSERT INTO workspaces (name) VALUES (?) RETURNING id`, corpus.Workspace)
	repo := insert(`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'api', 'acme/api') RETURNING id`, ws)
	api := app.NewService(smartdiffpg.NewStore(db))

	for i, tc := range g.Service {
		t.Run(tc.Name, func(t *testing.T) {
			askFor := tc.AskFor
			if tc.Tables.PR != nil {
				askFor = insert(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
					VALUES (?, ?, ?, 'Add rate limiting', 'dev', 'feat', 'main', 'a1b2c3d4e5f6') RETURNING id`, ws, repo, i+1)
			} else {
				askFor = "00000000-0000-4000-8000-000000000404"
			}
			for _, f := range tc.Tables.Files {
				exec(db, `INSERT INTO pr_files (pr_id, path, additions, deletions) VALUES (?, ?, ?, ?)`, askFor, f.Path, f.Additions, f.Deletions)
			}
			agents := map[string]string{}
			reviews := map[string]string{}
			for _, r := range tc.Tables.Reviews {
				var agent *string
				if r.AgentID != nil {
					id, ok := agents[*r.AgentID]
					if !ok {
						id = insert(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
							VALUES (?, ?, 'openai', 'gpt', 'be careful') RETURNING id`, ws, tc.Name+"/"+*r.AgentID)
						agents[*r.AgentID] = id
					}
					agent = &id
				}
				// The golden's "chat" stands for any kind but "review"; the column admits "summary".
				kind := r.Kind
				if kind != "review" {
					kind = "summary"
				}
				reviews[r.ID] = insert(`INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, created_at)
					VALUES (?, ?, ?, ?, ?::timestamptz) RETURNING id`, ws, askFor, agent, kind, r.CreatedAt)
			}
			for _, f := range tc.Tables.Findings {
				exec(db, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, dismissed_at)
					VALUES (?, ?, ?, ?, 'WARNING', 'bug', 't', 'r', 0.9, CASE WHEN ? THEN now() END)`,
					reviews[f.ReviewID], f.File, f.StartLine, f.StartLine, f.Dismissed)
			}

			status, body := getSmartDiff(t, api, ws, askFor)
			if tc.Error != nil {
				if status != http.StatusNotFound {
					t.Fatalf("GET smart-diff of an unknown pull request = %d %s, want 404", status, body)
				}
				var env struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("decode envelope %s: %v", body, err)
				}
				if env.Error.Code != tc.Error.Code || env.Error.Message != tc.Error.Message {
					t.Errorf("error = %+v, want %+v", env.Error, *tc.Error)
				}
				return
			}
			if status != tc.Status {
				t.Fatalf("GET smart-diff = %d %s, want %d", status, strings.TrimSpace(string(body)), tc.Status)
			}
			if diff := cmp.Diff(jsonValue(t, tc.Body), jsonValue(t, body)); diff != "" {
				t.Errorf("smart-diff body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
