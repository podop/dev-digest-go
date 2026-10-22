package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

const ws = "ws"

// wantErr asserts err is an apperr of the kind, code and message (code "" = the kind's default).
func wantErr(t *testing.T, err error, kind apperr.Kind, code, msg string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr of kind %s", err, kind)
	}
	if ae.Kind != kind || (code != "" && ae.Code != code) || ae.Message != msg {
		t.Errorf("error = {%s %q %q}, want {%s %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, msg)
	}
}

func TestStart_InvalidRequests(t *testing.T) {
	dup21 := make([]string, 21)
	for i := range dup21 {
		dup21[i] = "a"
	}
	tests := []struct {
		name string
		req  app.StartRequest
		msg  string
	}{
		{"empty", app.StartRequest{}, "Provide agentId, all:true or agentIds"},
		{"empty agentIds", app.StartRequest{AgentIDs: []string{}}, "agentIds must contain 1–20 ids"},
		{"21 copies", app.StartRequest{AgentIDs: dup21}, "agentIds must contain 1–20 ids"},
		{"agentIds with all", app.StartRequest{AgentIDs: []string{"a"}, All: true}, "Provide exactly one of agentId, all or agentIds"},
		{"agentIds with agentId", app.StartRequest{AgentIDs: []string{"a"}, AgentID: "a"}, "Provide exactly one of agentId, all or agentIds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withPull()
			_, err := h.svc.Start(t.Context(), ws, "pr", tc.req)
			wantErr(t, err, apperr.KindInvalidInput, "invalid_run_request", tc.msg)
			if len(h.j.all()) != 0 {
				t.Errorf("Start(%s) made calls %v, want none", tc.name, h.j.all())
			}
		})
	}
}

func TestStart_MultiAgentRun(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	h.withAgent("b", "Beta", false) // disabled agents may be named
	h.withAgent("c", "Gamma", true)

	got, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentIDs: []string{"a", "b", "a"}})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	parent := "parent-1"
	want := app.StartResult{PRID: "pr", MultiAgentRunID: &parent, Runs: []app.RunTarget{
		{RunID: "run-a", AgentID: "a", AgentName: "Alpha"},
		{RunID: "run-b", AgentID: "b", AgentName: "Beta"},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Start() mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []string{"tx begin", "create parent", "create run a", "create run b", "tx end", "executor start"}
	if diff := cmp.Diff(wantCalls, h.j.all()); diff != "" {
		t.Errorf("call order mismatch (-want +got):\n%s", diff)
	}
	for _, r := range h.store.createdRuns {
		if r.MultiAgentRunID == nil || *r.MultiAgentRunID != parent || r.WorkspaceID != ws || r.PRID != "pr" {
			t.Errorf("CreateRun(%+v): want parent %q, workspace, pr", r, parent)
		}
	}
	b := h.exec.batches[0]
	if b.WorkspaceID != ws || b.Pull.ID != "pr" || b.Repo.ID != "repo" || len(b.Jobs) != 2 ||
		b.Jobs[0].RunID != "run-a" || b.Jobs[1].Agent.ID != "b" {
		t.Errorf("batch = %+v, want both jobs of pull pr/repo in target order", b)
	}
}

func TestStart_NoParentRun(t *testing.T) {
	tests := []struct {
		name string
		req  app.StartRequest
		runs []string
	}{
		{"duplicate ids collapse to one run", app.StartRequest{AgentIDs: []string{"a", "a"}}, []string{"a"}},
		{"single agentId", app.StartRequest{AgentID: "b"}, []string{"b"}},
		{"all skips disabled agents", app.StartRequest{All: true}, []string{"a", "c"}},
		{"all wins over agentId", app.StartRequest{AgentID: "b", All: true}, []string{"a", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withPull()
			h.withAgent("a", "Alpha", true)
			h.withAgent("b", "Beta", false)
			h.withAgent("c", "Gamma", true)
			got, err := h.svc.Start(t.Context(), ws, "pr", tc.req)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			var ids []string
			for _, r := range got.Runs {
				ids = append(ids, r.AgentID)
			}
			if !slices.Equal(ids, tc.runs) {
				t.Errorf("Start() runs = %v, want %v", ids, tc.runs)
			}
			if got.MultiAgentRunID != nil || h.store.parents != 0 {
				t.Errorf("Start() created a multi-agent run %v, want none", got.MultiAgentRunID)
			}
		})
	}
}

func TestStart_AllWithNoEnabledAgent(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("b", "Beta", false)
	got, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{All: true})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got.PRID != "pr" || got.MultiAgentRunID != nil || got.Runs == nil || len(got.Runs) != 0 {
		t.Errorf("Start() = %+v, want pr with an empty non-nil run list", got)
	}
	if len(h.j.all()) != 0 {
		t.Errorf("Start() made calls %v, want no row and no executor start", h.j.all())
	}
}

func TestStart_NotFoundOrder(t *testing.T) {
	tests := []struct {
		name    string
		prep    func(h *harness)
		req     app.StartRequest
		message string
	}{
		{"unknown agent and unknown pull: agent first", func(h *harness) {},
			app.StartRequest{AgentID: "ghost"}, "Agent not found"},
		{"non-UUID agentId is a 404 too", func(h *harness) { h.withPull() },
			app.StartRequest{AgentID: "abc"}, "Agent not found"},
		{"one unknown id of agentIds", func(h *harness) { h.withPull(); h.withAgent("a", "A", true) },
			app.StartRequest{AgentIDs: []string{"a", "ghost"}}, "Agent not found"},
		{"known agent, unknown pull", func(h *harness) { h.withAgent("a", "A", true) },
			app.StartRequest{AgentID: "a"}, "Pull request not found"},
		{"unknown repo", func(h *harness) {
			h.withAgent("a", "A", true)
			h.store.pulls["pr"] = domain.ReviewPull{ID: "pr", RepoID: "gone"}
		}, app.StartRequest{AgentID: "a"}, "Repo not found"},
		{"all with no agent still needs the pull", func(h *harness) {},
			app.StartRequest{All: true}, "Pull request not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.prep(h)
			_, err := h.svc.Start(t.Context(), ws, "pr", tc.req)
			wantErr(t, err, apperr.KindNotFound, "", tc.message)
			if len(h.j.all()) != 0 {
				t.Errorf("Start() wrote %v before failing, want nothing", h.j.all())
			}
		})
	}
}

func TestStart_WriteFailureStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "A", true)
	h.store.createErr = errors.New("db down")
	_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"})
	if err == nil || !errors.Is(err, h.store.createErr) {
		t.Fatalf("Start() error = %v, want the CreateRun failure", err)
	}
	if len(h.exec.batches) != 0 {
		t.Errorf("executor started %d batches after a failed write, want none", len(h.exec.batches))
	}
}

func TestStart_RolledBackTransactionStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "A", true)
	h.tx.fail = errors.New("commit failed")
	if _, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"}); !errors.Is(err, h.tx.fail) {
		t.Fatalf("Start() error = %v, want the commit failure", err)
	}
	if len(h.exec.batches) != 0 {
		t.Errorf("executor started after a rolled-back transaction")
	}
}

func TestListings_NilBecomeEmpty(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	active, err := h.svc.ActiveRuns(t.Context(), ws, "pr")
	if err != nil || active == nil || len(active) != 0 {
		t.Errorf("ActiveRuns() = %v, %v, want an empty non-nil slice", active, err)
	}
	runs, err := h.svc.ListRuns(t.Context(), ws, "pr")
	if err != nil || runs == nil || len(runs) != 0 {
		t.Errorf("ListRuns() = %v, %v, want an empty non-nil slice", runs, err)
	}
	est, err := h.svc.Estimates(t.Context(), ws)
	if err != nil || est == nil || len(est) != 0 {
		t.Errorf("Estimates() = %v, %v, want an empty non-nil slice", est, err)
	}
}

func TestDeleteRun(t *testing.T) {
	for _, existed := range []bool{true, false} {
		h := newHarness(t)
		h.store.deleteRunOK = existed
		got, err := h.svc.DeleteRun(t.Context(), ws, "run-x")
		if err != nil || got != existed {
			t.Errorf("DeleteRun() = %v, %v, want %v", got, err, existed)
		}
		if diff := cmp.Diff([]string{"tx begin", "delete run", "tx end"}, h.j.all()); diff != "" {
			t.Errorf("DeleteRun() must run in one transaction (-want +got):\n%s", diff)
		}
	}
}

func TestCancelRun(t *testing.T) {
	h := newHarness(t)
	h.store.runs["r"] = domain.RunState{ID: "r", Status: domain.StatusRunning}
	if err := h.svc.CancelRun(t.Context(), ws, "r"); err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	want := []string{"bus publish info", "bus cancel", "store cancel", "bus complete"}
	if diff := cmp.Diff(want, h.j.all()); diff != "" {
		t.Errorf("CancelRun() call order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Cancellation requested — stopping…"}, h.bus.published); diff != "" {
		t.Errorf("published mismatch (-want +got):\n%s", diff)
	}
	if h.store.cancelNote != "Cancelled by user" {
		t.Errorf("CancelIfRunning note = %q, want %q", h.store.cancelNote, "Cancelled by user")
	}
}

func TestCancelRun_UnknownRunTouchesNothing(t *testing.T) {
	h := newHarness(t)
	err := h.svc.CancelRun(t.Context(), ws, "ghost")
	wantErr(t, err, apperr.KindNotFound, "", "Run not found")
	if len(h.j.all()) != 0 {
		t.Errorf("CancelRun() touched %v, want nothing", h.j.all())
	}
}

func TestCancelRun_FinishedRunStaysAsItIs(t *testing.T) {
	h := newHarness(t)
	h.store.runs["r"] = domain.RunState{ID: "r", Status: domain.StatusDone}
	if err := h.svc.CancelRun(t.Context(), ws, "r"); err != nil {
		t.Fatalf("CancelRun() error = %v, want nil for a finished run", err)
	}
}

func drain(t *testing.T, sub app.Subscription) []domain.RunEvent {
	t.Helper()
	var evs []domain.RunEvent
	for ev := range sub.C() {
		evs = append(evs, ev)
	}
	return evs
}

func TestEvents_UnknownRun(t *testing.T) {
	h := newHarness(t)
	sub, err := h.svc.Events(t.Context(), ws, "ghost")
	wantErr(t, err, apperr.KindNotFound, "", "Run not found")
	if sub != nil {
		t.Errorf("Events() = %v, want no subscription", sub)
	}
}

func TestEvents_Live(t *testing.T) {
	tests := []struct {
		name   string
		status domain.RunStatus
		onBus  bool
	}{
		{"held by the bus", domain.StatusDone, true},
		{"running in the database, not on the bus yet", domain.StatusRunning, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.runs["r"] = domain.RunState{ID: "r", Status: tc.status}
			h.bus.held["r"] = tc.onBus
			sub, err := h.svc.Events(t.Context(), ws, "r")
			if err != nil {
				t.Fatalf("Events() error = %v", err)
			}
			defer sub.Close()
			if !slices.Equal(h.bus.subbed, []string{"r"}) {
				t.Errorf("Events() subscribed to %v, want the bus run r", h.bus.subbed)
			}
		})
	}
}

func TestEvents_FinishedRunNotOnTheBus(t *testing.T) {
	boom, empty := "boom", ""
	tests := []struct {
		name   string
		status domain.RunStatus
		err    *string
		kind   reviewer.EventKind
		msg    string
		data   string
	}{
		{"done", domain.StatusDone, nil, reviewer.EventResult, "Run finished", `{"status":"done","error":null}`},
		{"failed with a note", domain.StatusFailed, &boom, reviewer.EventInfo, "Run failed: boom", `{"status":"failed","error":"boom"}`},
		{"reaped, no note", domain.StatusFailed, nil, reviewer.EventInfo, "Run failed", `{"status":"failed","error":null}`},
		{"cancelled", domain.StatusCancelled, &empty, reviewer.EventInfo, "Run cancelled", `{"status":"cancelled","error":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.runs["r"] = domain.RunState{ID: "r", Status: tc.status, Error: tc.err}
			sub, err := h.svc.Events(t.Context(), ws, "r")
			if err != nil {
				t.Fatalf("Events() error = %v", err)
			}
			defer sub.Close()
			evs := drain(t, sub)
			if len(evs) != 1 {
				t.Fatalf("Events() yielded %d events, want exactly 1", len(evs))
			}
			ev := evs[0]
			if ev.RunID != "r" || ev.Seq != 1 || ev.Kind != tc.kind || ev.Msg != tc.msg || ev.T != "12:30:45" {
				t.Errorf("event = {%s %d %s %q %s}, want {r 1 %s %q 12:30:45}", ev.RunID, ev.Seq, ev.Kind, ev.Msg, ev.T, tc.kind, tc.msg)
			}
			if string(ev.Data) != tc.data {
				t.Errorf("event data = %s, want %s", ev.Data, tc.data)
			}
			if len(h.bus.subbed) != 0 {
				t.Errorf("Events() subscribed to the bus for a forgotten run")
			}
			sub.Close() // twice is safe
		})
	}
}

func TestTrace(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		h := newHarness(t)
		h.store.trace = domain.Trace{Ended: true, RawOutput: "x"}
		got, err := h.svc.Trace(t.Context(), ws, "r")
		if err != nil || !got.Ended || got.RawOutput != "x" {
			t.Errorf("Trace() = %+v, %v, want the stored trace", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		h := newHarness(t)
		h.store.traceErr = domain.ErrNotFound
		_, err := h.svc.Trace(t.Context(), ws, "r")
		wantErr(t, err, apperr.KindNotFound, "", "Run trace not found")
	})
	t.Run("invalid document is a logged 500 without content", func(t *testing.T) {
		h := newHarness(t)
		h.store.traceErr = errors.Join(domain.ErrTraceInvalid, errors.New("secret-document-text"))
		var buf bytes.Buffer
		svc := app.NewService(app.Deps{
			Store: h.store, Tx: h.tx, Executor: h.exec, Bus: h.bus,
			Log: slog.New(slog.NewTextHandler(&buf, nil)),
		})
		_, err := svc.Trace(t.Context(), ws, "run-7")
		wantErr(t, err, apperr.KindInternal, "internal_error", "Internal error")
		if err.Error() != "Internal error" {
			t.Errorf("Trace() error text = %q, want only the client-safe message", err.Error())
		}
		logged := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("run-7")) || bytes.Contains(buf.Bytes(), []byte("secret-document-text")) {
			t.Errorf("log = %q, want the run id and no document content", logged)
		}
	})
}

func TestReviews(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Reviews(t.Context(), ws, "pr")
	wantErr(t, err, apperr.KindNotFound, "", "Pull request not found")

	h.withPull()
	got, err := h.svc.Reviews(t.Context(), ws, "pr")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Reviews() = %v, %v, want an empty non-nil slice", got, err)
	}
	h.store.reviews = []domain.ReviewRecord{{ID: "r1"}}
	got, _ = h.svc.Reviews(t.Context(), ws, "pr")
	if len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("Reviews() = %+v, want review r1", got)
	}
}

func TestDeleteReview(t *testing.T) {
	h := newHarness(t)
	h.store.deletedRev["r1"] = true
	if err := h.svc.DeleteReview(t.Context(), ws, "r1"); err != nil {
		t.Errorf("DeleteReview(r1) error = %v", err)
	}
	wantErr(t, h.svc.DeleteReview(t.Context(), ws, "r2"), apperr.KindNotFound, "", "Review not found")
}

func TestAcceptAndDismissFinding(t *testing.T) {
	h := newHarness(t)
	h.store.findings["f"] = domain.FindingRecord{ID: "f", Title: "t"}

	acc, err := h.svc.AcceptFinding(t.Context(), ws, "f")
	if err != nil || acc.AcceptedAt == nil || !acc.AcceptedAt.Equal(h.now) {
		t.Errorf("AcceptFinding() = %+v, %v, want accepted_at = %v", acc, err, h.now)
	}
	dis, err := h.svc.DismissFinding(t.Context(), ws, "f")
	if err != nil || dis.DismissedAt == nil || !dis.DismissedAt.Equal(h.now) {
		t.Errorf("DismissFinding() = %+v, %v, want dismissed_at = %v", dis, err, h.now)
	}
	_, err = h.svc.AcceptFinding(t.Context(), ws, "ghost")
	wantErr(t, err, apperr.KindNotFound, "", "Finding not found")
	_, err = h.svc.DismissFinding(t.Context(), ws, "ghost")
	wantErr(t, err, apperr.KindNotFound, "", "Finding not found")
}

func TestMultiAgentRun(t *testing.T) {
	h := newHarness(t)
	h.store.headerErr = domain.ErrNotFound
	_, err := h.svc.MultiAgentRun(t.Context(), ws, "m")
	wantErr(t, err, apperr.KindNotFound, "multi_agent_run_not_found", "Multi-agent run not found")

	h.store.headerErr = nil
	h.store.header = domain.MultiAgentHeader{ID: "m", PRID: "pr"}
	d1, d2 := 100, 250
	h.store.columns = []domain.AgentColumn{
		{RunID: "r2", AgentName: "beta", Status: domain.StatusDone, DurationMS: &d2},
		{RunID: "r1", AgentName: "Alpha", Status: domain.StatusDone, DurationMS: &d1},
	}
	got, err := h.svc.MultiAgentRun(t.Context(), ws, "m")
	if err != nil {
		t.Fatalf("MultiAgentRun() error = %v", err)
	}
	if got.ID != "m" || got.AgentCount != 2 || got.TotalDurationMS != 250 || got.TotalCostUSD != nil {
		t.Errorf("MultiAgentRun() = %+v, want 2 columns, 250 ms, no cost", got.MultiAgentHeader)
	}
	if got.Columns[0].RunID != "r1" || got.Columns[1].RunID != "r2" {
		t.Errorf("columns = %s,%s, want ordered by agent name (English collation) r1,r2", got.Columns[0].RunID, got.Columns[1].RunID)
	}
	if got.Conflicts == nil {
		t.Errorf("Conflicts = nil, want an empty non-nil slice")
	}
}

func TestListMultiAgentRuns(t *testing.T) {
	ptr := func(n int) *int { return &n }
	for _, tc := range []struct {
		name  string
		limit *int
		want  int
	}{{"default", nil, 20}, {"explicit", ptr(5), 5}, {"upper bound", ptr(20), 20}, {"lower bound", ptr(1), 1}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.repoOK = true
			got, err := h.svc.ListMultiAgentRuns(t.Context(), ws, "repo", tc.limit)
			if err != nil || got == nil || h.store.listedLimit != tc.want {
				t.Errorf("ListMultiAgentRuns() = %v, %v with store limit %d, want empty non-nil with limit %d", got, err, h.store.listedLimit, tc.want)
			}
		})
	}
	for _, bad := range []int{0, 21, -1} {
		h := newHarness(t)
		h.store.repoOK = false // the limit is checked first
		_, err := h.svc.ListMultiAgentRuns(t.Context(), ws, "repo", &bad)
		wantErr(t, err, apperr.KindInvalidInput, "invalid_limit", "limit must be between 1 and 20")
	}
	h := newHarness(t)
	_, err := h.svc.ListMultiAgentRuns(t.Context(), ws, "repo", nil)
	wantErr(t, err, apperr.KindNotFound, "", "Repo not found")
}

func learnHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	sugg := "use a mutex"
	h.store.learnTarget = domain.LearnTarget{
		Finding:  domain.FindingRecord{ID: "f", Title: "Race", Rationale: "unsynchronised", Suggestion: &sugg, Confidence: 0.8},
		RepoID:   "repo",
		PRNumber: 482,
	}
	return h
}

func TestLearn_CreatesTheMemoryOnce(t *testing.T) {
	h := learnHarness(t)
	got, err := h.svc.Learn(t.Context(), ws, "f")
	if err != nil {
		t.Fatalf("Learn() error = %v", err)
	}
	if got.MemoryID != "mem-f" || got.Finding.MemoryID == nil || *got.Finding.MemoryID != "mem-f" || got.Finding.ID != "f" {
		t.Errorf("Learn() = %+v, want finding f carrying memory mem-f", got)
	}
	want := domain.NewMemory{
		WorkspaceID: ws, RepoID: "repo", FindingID: "f", PRNumber: 482,
		Content: "Race\n\nunsynchronised\n\nSuggested fix: use a mutex", Confidence: 0.8,
	}
	if diff := cmp.Diff([]domain.NewMemory{want}, h.store.insertedMems); diff != "" {
		t.Errorf("inserted memory mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []string{"tx begin", "lock learn", "lookup memory", "insert memory", "tx end"}
	if diff := cmp.Diff(wantCalls, h.j.all()); diff != "" {
		t.Errorf("Learn() must lock, look up, insert inside one transaction (-want +got):\n%s", diff)
	}

	again, err := h.svc.Learn(t.Context(), ws, "f")
	if err != nil || again.MemoryID != "mem-f" || len(h.store.insertedMems) != 1 {
		t.Errorf("second Learn() = %+v, %v with %d inserts, want the same memory and no new insert", again, err, len(h.store.insertedMems))
	}
}

func TestLearn_ConcurrentCallsShareOneMemory(t *testing.T) {
	h := learnHarness(t)
	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			r, err := h.svc.Learn(context.Background(), ws, "f")
			ids[i], errs[i] = r.MemoryID, err
		})
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || ids[i] != "mem-f" {
			t.Errorf("Learn() #%d = %q, %v, want mem-f", i, ids[i], errs[i])
		}
	}
	if len(h.store.insertedMems) != 1 {
		t.Errorf("%d concurrent Learn calls inserted %d memories, want 1", n, len(h.store.insertedMems))
	}
}

func TestLearn_UnknownFinding(t *testing.T) {
	h := learnHarness(t)
	h.store.learnErr = domain.ErrNotFound
	_, err := h.svc.Learn(t.Context(), ws, "ghost")
	wantErr(t, err, apperr.KindNotFound, "", "Finding not found")
	if len(h.j.all()) != 0 {
		t.Errorf("Learn() opened a transaction for an unknown finding: %v", h.j.all())
	}
}

func TestFailInterrupted(t *testing.T) {
	h := newHarness(t)
	h.store.failedN = 3
	n, err := h.svc.FailInterrupted(t.Context())
	if err != nil || n != 3 {
		t.Errorf("FailInterrupted() = %d, %v, want 3, nil", n, err)
	}
	h.store.failedErr = errors.New("no table")
	if _, err := h.svc.FailInterrupted(t.Context()); !errors.Is(err, h.store.failedErr) {
		t.Errorf("FailInterrupted() error = %v, want the store failure", err)
	}
}

func TestNewService_PanicsOnMissingDependency(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewService(Deps{}) did not panic")
		}
	}()
	app.NewService(app.Deps{})
}
