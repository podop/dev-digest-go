package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"testing"
	"time"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

const ws = "ws-1"

var epoch = time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)

func fixedClock() app.Clock { return func() time.Time { return epoch } }

// logCapture returns a logger writing JSON lines and a function that decodes them.
func logCapture(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return log, func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("log line %q is not JSON: %v", line, err)
			}
			out = append(out, m)
		}
		return out
	}
}

// wantApp fails unless err is an apperr.Error of the kind, code and message.
func wantApp(t *testing.T, err error, kind apperr.Kind, code, message string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v (%T), want an apperr.Error {%v %s %q}", err, err, kind, code, message)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != message {
		t.Fatalf("error = {%v %s %q}, want {%v %s %q}", ae.Kind, ae.Code, ae.Message, kind, code, message)
	}
	return ae
}

// detailsJSON renders the details of an application error the way the response does.
func detailsJSON(t *testing.T, ae *apperr.Error) string {
	t.Helper()
	b, err := json.Marshal(ae.Details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	return string(b)
}

type ownerKey struct {
	kind domain.OwnerKind
	id   string
}

// fakeCases is an in-memory app.CaseStore.
type fakeCases struct {
	owners  map[ownerKey]bool
	cases   []domain.Case
	last    map[string]*domain.LastResult
	nextID  int
	updates int // UpdateCase calls
	writes  int // updates whose result differed from the stored case
	// raceWinner, when set, makes CreateCase fail as a lost race on source_finding_id.
	raceWinner string
	// counts overrides CountCases and AgentCaseCounts when non-nil.
	counts map[ownerKey]int
	err    error
}

func newFakeCases() *fakeCases {
	return &fakeCases{owners: map[ownerKey]bool{}, last: map[string]*domain.LastResult{}}
}

func (f *fakeCases) addOwner(kind domain.OwnerKind, id string) { f.owners[ownerKey{kind, id}] = true }

func (f *fakeCases) add(c domain.Case) domain.Case {
	f.nextID++
	if c.ID == "" {
		c.ID = "case-" + strconv.Itoa(f.nextID)
	}
	c.WorkspaceID = ws
	f.cases = append(f.cases, c)
	return c
}

func (f *fakeCases) find(id string) (int, bool) {
	for i, c := range f.cases {
		if c.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (f *fakeCases) OwnerExists(_ context.Context, _ string, kind domain.OwnerKind, id string) (bool, error) {
	return f.owners[ownerKey{kind, id}], f.err
}

func (f *fakeCases) owned(kind domain.OwnerKind, id string) []domain.Case {
	var out []domain.Case
	for _, c := range f.cases {
		if c.OwnerKind == kind && c.OwnerID == id {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeCases) CountCases(_ context.Context, _ string, kind domain.OwnerKind, id string) (int, error) {
	if n, ok := f.counts[ownerKey{kind, id}]; ok {
		return n, nil
	}
	return len(f.owned(kind, id)), f.err
}

func (f *fakeCases) CaseIDs(_ context.Context, _ string, kind domain.OwnerKind, id string) ([]string, error) {
	var ids []string
	for _, c := range f.owned(kind, id) {
		ids = append(ids, c.ID)
	}
	return ids, f.err
}

func (f *fakeCases) AgentCaseCounts(context.Context, string) (map[string]int, error) {
	out := map[string]int{}
	for k, n := range f.counts {
		if k.kind == domain.OwnerAgent {
			out[k.id] = n
		}
	}
	return out, f.err
}

func (f *fakeCases) ListCases(_ context.Context, _ string, kind domain.OwnerKind, id string) ([]domain.CaseView, error) {
	var out []domain.CaseView
	for _, c := range f.owned(kind, id) {
		out = append(out, domain.CaseView{Case: c, LastResult: f.last[c.ID]})
	}
	return out, f.err
}

func (f *fakeCases) GetCase(_ context.Context, _, id string) (domain.Case, error) {
	if i, ok := f.find(id); ok {
		return f.cases[i], f.err
	}
	return domain.Case{}, domain.ErrNotFound
}

func (f *fakeCases) GetCaseView(ctx context.Context, w, id string) (domain.CaseView, error) {
	c, err := f.GetCase(ctx, w, id)
	return domain.CaseView{Case: c, LastResult: f.last[id]}, err
}

func (f *fakeCases) CaseBySourceFinding(_ context.Context, _, finding string) (string, bool, error) {
	for _, c := range f.cases {
		if c.SourceFindingID != nil && *c.SourceFindingID == finding {
			return c.ID, true, f.err
		}
	}
	return "", false, f.err
}

func (f *fakeCases) CreateCase(_ context.Context, n domain.NewCase) (domain.Case, error) {
	if f.err != nil {
		return domain.Case{}, f.err
	}
	if f.raceWinner != "" {
		return domain.Case{}, fmt.Errorf("insert: %w", &domain.CaseExistsError{CaseID: f.raceWinner})
	}
	return f.add(domain.Case{
		OwnerKind: n.OwnerKind, OwnerID: n.OwnerID, Name: n.Name, Notes: n.Notes, Expectation: n.Expectation,
		InputDiff: n.InputDiff, InputMeta: n.InputMeta, CreatedAt: epoch,
		SourceFindingID: n.SourceFindingID, SourceReviewID: n.SourceReviewID, SourcePRID: n.SourcePRID,
	}), nil
}

func (f *fakeCases) UpdateCase(_ context.Context, _, id string, fn func(domain.Case) (domain.Case, error)) (domain.Case, error) {
	f.updates++
	i, ok := f.find(id)
	if !ok {
		return domain.Case{}, domain.ErrNotFound
	}
	stored := f.cases[i]
	next, err := fn(stored)
	if err != nil {
		return domain.Case{}, err
	}
	if !casesEqual(stored, next) {
		f.writes++
		f.cases[i] = next
	}
	return next, nil
}

func casesEqual(a, b domain.Case) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (f *fakeCases) DeleteCase(_ context.Context, _, id string) error {
	i, ok := f.find(id)
	if !ok {
		return domain.ErrNotFound
	}
	f.cases = slices.Delete(f.cases, i, i+1)
	return nil
}

// fakeFindings is an in-memory app.Findings.
type fakeFindings map[string]domain.FindingSource

func (f fakeFindings) Source(_ context.Context, _, id string) (domain.FindingSource, error) {
	if s, ok := f[id]; ok {
		return s, nil
	}
	return domain.FindingSource{}, domain.ErrNotFound
}

// fakeSnapshots is an in-memory app.Snapshots.
type fakeSnapshots struct {
	agents   map[string]domain.AgentSnapshot
	skills   map[string]domain.SkillSnapshot
	order    []string
	vanished map[string]bool // listed by AgentIDs but gone by the time Agent is read
}

func (f *fakeSnapshots) Agent(_ context.Context, _, id string) (domain.AgentSnapshot, error) {
	a, ok := f.agents[id]
	if !ok || f.vanished[id] {
		return domain.AgentSnapshot{}, domain.ErrNotFound
	}
	return a, nil
}

func (f *fakeSnapshots) Skill(_ context.Context, _, id string) (domain.SkillSnapshot, error) {
	if s, ok := f.skills[id]; ok {
		return s, nil
	}
	return domain.SkillSnapshot{}, domain.ErrNotFound
}

func (f *fakeSnapshots) AgentIDs(context.Context, string) ([]string, error) { return f.order, nil }

// fakeLauncher records launched runs.
type fakeLauncher struct{ runs []domain.Run }

func (f *fakeLauncher) Launch(_ context.Context, r domain.Run) { f.runs = append(f.runs, r) }

// finishedQuery is one FinishedAgentRuns/ListAgentRuns call.
type finishedQuery struct {
	agent string
	limit int
	since *time.Time
}

// fakeRuns is an in-memory app.RunStore; the read methods return what the test configured.
type fakeRuns struct {
	runs      map[string]domain.Run
	results   map[string][]domain.CaseResult
	active    map[ownerKey]bool
	created   []domain.NewRun
	finished  map[string][]domain.Run // per agent, newest first
	agentRuns []domain.Run
	skillRuns []domain.Run
	dashboard []domain.AgentRef
	recent    []domain.RecentRun
	reaped    int
	reapErr   error
	createErr error // returned by CreateRun when set
	queries   []finishedQuery
	nextID    int
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{
		runs: map[string]domain.Run{}, results: map[string][]domain.CaseResult{},
		active: map[ownerKey]bool{}, finished: map[string][]domain.Run{},
	}
}

func (f *fakeRuns) CreateRun(_ context.Context, n domain.NewRun) (domain.Run, error) {
	if f.createErr != nil {
		return domain.Run{}, f.createErr
	}
	key := ownerKey{n.OwnerKind, n.AgentID}
	if n.OwnerKind == domain.OwnerSkill {
		key.id = *n.SkillID
	}
	if f.active[key] {
		return domain.Run{}, fmt.Errorf("insert: %w", domain.ErrRunActive)
	}
	f.active[key] = true
	f.created = append(f.created, n)
	f.nextID++
	r := domain.Run{
		ID: "run-" + strconv.Itoa(f.nextID), WorkspaceID: n.WorkspaceID, OwnerKind: n.OwnerKind,
		SkillID: n.SkillID, SkillVersion: n.SkillVersion, AgentID: n.AgentID, AgentVersion: n.AgentVersion,
		AgentName: n.AgentName, Config: n.Config, CaseIDs: n.CaseIDs, Status: domain.RunRunning,
		StartedAt: epoch, CasesTotal: len(n.CaseIDs),
	}
	f.runs[r.ID] = r
	return r, nil
}

func (f *fakeRuns) GetRun(_ context.Context, _, id string) (domain.Run, error) {
	if r, ok := f.runs[id]; ok {
		return r, nil
	}
	return domain.Run{}, domain.ErrNotFound
}

func (f *fakeRuns) RunResults(_ context.Context, _, id string) ([]domain.CaseResult, error) {
	return f.results[id], nil
}

func (f *fakeRuns) ListAgentRuns(_ context.Context, _, agent string, limit int, since *time.Time) ([]domain.Run, error) {
	f.queries = append(f.queries, finishedQuery{agent, limit, since})
	return f.agentRuns, nil
}

func (f *fakeRuns) ListSkillRuns(context.Context, string, string, int) ([]domain.Run, error) {
	return f.skillRuns, nil
}

func (f *fakeRuns) FinishedAgentRuns(_ context.Context, _, agent string, limit int, since *time.Time) ([]domain.Run, error) {
	f.queries = append(f.queries, finishedQuery{agent, limit, since})
	list := f.finished[agent]
	if limit < len(list) {
		list = list[:limit]
	}
	return list, nil
}

func (f *fakeRuns) FinishedRunsByAgent(_ context.Context, _ string, agents []string, limit int) (map[string][]domain.Run, error) {
	out := map[string][]domain.Run{}
	for _, agent := range agents {
		f.queries = append(f.queries, finishedQuery{agent, limit, nil})
		list := f.finished[agent]
		if limit < len(list) {
			list = list[:limit]
		}
		if len(list) > 0 {
			out[agent] = list
		}
	}
	return out, nil
}

func (f *fakeRuns) DashboardAgents(context.Context, string) ([]domain.AgentRef, error) {
	return f.dashboard, nil
}

func (f *fakeRuns) RecentAgentRuns(_ context.Context, _ string, limit int) ([]domain.RecentRun, error) {
	f.queries = append(f.queries, finishedQuery{"recent", limit, nil})
	return f.recent, nil
}

func (f *fakeRuns) RecordResult(context.Context, string, domain.NewResult) error { return nil }
func (f *fakeRuns) DropRunCase(context.Context, string, string) error            { return nil }
func (f *fakeRuns) FinishRun(context.Context, string, string, domain.RunFinish) (bool, error) {
	return false, nil
}

func (f *fakeRuns) FailInterrupted(context.Context) (int, error) { return f.reaped, f.reapErr }

var errBoom = errors.New("boom")
