package performancehttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
)

// scenario is one fold scenario of testdata/node.json: the facts of a workspace, the query and
// the bodies the Node service produced for them (generator: ../../testdata/gen.mts).
type scenario struct {
	Name   string            `json:"name"`
	Query  map[string]string `json:"query"`
	Agents []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agents"`
	Runs []struct {
		AgentID    *string  `json:"agentId"`
		Status     string   `json:"status"`
		Source     string   `json:"source"`
		Model      *string  `json:"model"`
		DurationMs *int     `json:"durationMs"`
		CostUSD    *float64 `json:"costUsd"`
		RanAtMs    int64    `json:"ranAtMs"`
		Findings   *struct {
			Total, Accepted, Dismissed int
		} `json:"findings"`
		Facts []struct {
			Severity, Category string
			Count              int
		} `json:"facts"`
	} `json:"runs"`
	Skills []struct {
		SkillID string `json:"skillId"`
		Name    string `json:"name"`
		Runs    int    `json:"runs"`
	} `json:"skills"`
	Memory  []json.RawMessage `json:"memory"`
	History []struct {
		RunID          string `json:"run_id"`
		RanAt          string `json:"ran_at"`
		Status, Source string
		PR             *struct {
			ID     string `json:"id"`
			RepoID string `json:"repo_id"`
			Number int    `json:"number"`
		} `json:"pr"`
		Tokens        *int     `json:"tokens"`
		CostUSD       *float64 `json:"cost_usd"`
		FindingsCount *int     `json:"findings_count"`
		HasTrace      bool     `json:"has_trace"`
	} `json:"history"`
	Expected struct {
		Dashboard json.RawMessage            `json:"dashboard"`
		Stats     map[string]json.RawMessage `json:"stats"`
		Unknown   string                     `json:"unknown"`
	} `json:"expected"`
}

// scenarioReader is app.Reader over a scenario, filtering like the SQL does: a counted run is in
// the window and, when an agent is given, that agent's.
type scenarioReader struct{ s *scenario }

var _ app.Reader = scenarioReader{}

func (r scenarioReader) ListAgents(context.Context, string) ([]domain.AgentRef, error) {
	var out []domain.AgentRef
	for _, a := range r.s.Agents {
		out = append(out, domain.AgentRef{ID: a.ID, Name: a.Name})
	}
	return out, nil
}

func (r scenarioReader) FindAgent(_ context.Context, _, id string) (domain.AgentRef, error) {
	for _, a := range r.s.Agents {
		if a.ID == id {
			return domain.AgentRef{ID: a.ID, Name: a.Name}, nil
		}
	}
	return domain.AgentRef{}, domain.ErrNotFound
}

func (r scenarioReader) counted(win domain.PeriodWindow, agentID string, visit func(i int)) {
	for i, run := range r.s.Runs {
		if win.Contains(time.UnixMilli(run.RanAtMs)) && (agentID == "" || (run.AgentID != nil && *run.AgentID == agentID)) {
			visit(i)
		}
	}
}

func (r scenarioReader) CountedRuns(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.RunFact, error) {
	var out []domain.RunFact
	r.counted(win, agentID, func(i int) {
		run := r.s.Runs[i]
		out = append(out, domain.RunFact{
			AgentID: run.AgentID, Status: domain.RunStatus(run.Status), Source: domain.RunSource(run.Source),
			Model: run.Model, DurationMs: run.DurationMs, CostUSD: run.CostUSD, RanAt: time.UnixMilli(run.RanAtMs),
		})
	})
	return out, nil
}

func (r scenarioReader) FindingCounts(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.FindingCounts, error) {
	var out []domain.FindingCounts
	index := map[string]int{}
	r.counted(win, agentID, func(i int) {
		run := r.s.Runs[i]
		if run.Findings == nil || run.Findings.Total == 0 { // a run without findings adds no row
			return
		}
		key := "<nil>"
		if run.AgentID != nil {
			key = *run.AgentID
		}
		at, ok := index[key]
		if !ok {
			at = len(out)
			index[key] = at
			out = append(out, domain.FindingCounts{AgentID: run.AgentID})
		}
		out[at].Total += run.Findings.Total
		out[at].Accepted += run.Findings.Accepted
		out[at].Dismissed += run.Findings.Dismissed
	})
	return out, nil
}

func (r scenarioReader) FindingFacts(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.FindingFact, error) {
	var out []domain.FindingFact
	r.counted(win, agentID, func(i int) {
		for _, f := range r.s.Runs[i].Facts {
			out = append(out, domain.FindingFact{RanAt: time.UnixMilli(r.s.Runs[i].RanAtMs), Severity: f.Severity, Category: f.Category, Count: f.Count})
		}
	})
	return out, nil
}

func (r scenarioReader) SkillUsage(context.Context, string, domain.PeriodWindow, string) ([]domain.SkillFact, error) {
	var out []domain.SkillFact
	for _, k := range r.s.Skills {
		out = append(out, domain.SkillFact{SkillID: k.SkillID, Name: k.Name, Runs: k.Runs})
	}
	return out, nil
}

func (r scenarioReader) MemoryPulled(context.Context, string, domain.PeriodWindow, string) ([]json.RawMessage, error) {
	return r.s.Memory, nil
}

func (r scenarioReader) RunHistory(_ context.Context, _ string, _ domain.PeriodWindow, _ string, _ int) ([]domain.RunHistoryItem, error) {
	var out []domain.RunHistoryItem
	for _, h := range r.s.History {
		at, err := time.Parse(time.RFC3339Nano, h.RanAt)
		if err != nil {
			return nil, err
		}
		item := domain.RunHistoryItem{
			RunID: h.RunID, RanAt: at, Status: domain.RunStatus(h.Status), Source: domain.RunSource(h.Source),
			Tokens: h.Tokens, CostUSD: h.CostUSD, FindingsCount: h.FindingsCount, HasTrace: h.HasTrace,
		}
		if h.PR != nil {
			item.PR = &domain.RunPR{ID: h.PR.ID, RepoID: h.PR.RepoID, Number: h.PR.Number}
		}
		out = append(out, item)
	}
	return out, nil
}

// TestParity_FoldsMatchNode serves every scenario through the real use cases and routes and
// compares the response bodies with the Node bodies byte for byte: values, number formatting and
// key order (AC5055, NFR5001).
func TestParity_FoldsMatchNode(t *testing.T) {
	b, err := os.ReadFile("../../testdata/node.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Meta  struct{ Now int64 } `json:"meta"`
		Folds []scenario          `json:"folds"`
	}
	if err := json.Unmarshal(b, &corpus); err != nil {
		t.Fatalf("decode node.json: %v", err)
	}
	if len(corpus.Folds) < 50 {
		t.Fatalf("corpus has %d scenarios, a generator change shrank it", len(corpus.Folds))
	}
	now := time.UnixMilli(corpus.Meta.Now)

	for i := range corpus.Folds {
		s := &corpus.Folds[i]
		t.Run(s.Name, func(t *testing.T) {
			svc := app.NewService(scenarioReader{s}, func() time.Time { return now })
			srv := newServer(t, svc)
			q := url.Values{}
			for k, v := range s.Query {
				q.Set(k, v)
			}
			qs := "?" + q.Encode()

			compare := func(path string, want json.RawMessage) {
				t.Helper()
				r := get(t, srv, path+qs)
				if r.Status != http.StatusOK {
					t.Fatalf("GET %s%s = %d %s", path, qs, r.Status, r.Body)
				}
				if got := strings.TrimSpace(r.Body); got != string(want) {
					var g, w any
					_ = json.Unmarshal([]byte(got), &g)
					_ = json.Unmarshal(want, &w)
					t.Errorf("GET %s%s differs from Node (-node +go):\n%s\n(same values, different bytes: %v)",
						path, qs, cmp.Diff(w, g), cmp.Diff(w, g) == "")
				}
			}
			compare("/agents/performance", s.Expected.Dashboard)
			for _, a := range s.Agents {
				compare("/agents/"+a.ID+"/stats", s.Expected.Stats[a.ID])
			}
			r := get(t, srv, "/agents/00000000-0000-4000-8000-000000999999/stats"+qs)
			if r.Status != http.StatusNotFound || !strings.Contains(r.Body, `"`+s.Expected.Unknown+`"`) {
				t.Errorf("GET stats of an unknown agent = %d %s, want 404 %q", r.Status, r.Body, s.Expected.Unknown)
			}
		})
	}
}
