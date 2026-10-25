package main

import (
	"net/http"
	"testing"
	"time"
)

// Wiring of the spend context (plan S26): GET /settings/spend and GET /about are served, the cap
// is read from the settings bag, and an entry point that passes through the budget port answers
// 402 once the cap is reached. Since the cost ledger the month spend comes from the stored month
// totals, which TestSpend_StatusAndAdmissionReadTheLedger drives through seedLedgerSpend.

func TestSpend_RoutesAndCapAreWired(t *testing.T) {
	s := startStack(t, spec{now: func() time.Time { return time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC) }})

	st := s.get("/settings/spend")
	if st.Status != http.StatusOK {
		t.Fatalf("GET /settings/spend = %d %s, want 200", st.Status, st.Body)
	}
	body := st.Obj(t)
	if body["month_start"] != "2031-01-01T00:00:00.000Z" || body["cap_usd"] != nil || body["cap_reached"] != false {
		t.Errorf("GET /settings/spend without a cap = %s, want month_start 2031-01-01T00:00:00.000Z, cap_usd null, cap_reached false", st.Body)
	}

	if put := s.do(http.MethodPut, "/settings", map[string]any{"monthly_spend_cap_usd": 12.34}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings cap = %d %s, want 200", put.Status, put.Body)
	}
	if got := s.get("/settings/spend").Obj(t)["cap_usd"]; got != 12.34 {
		t.Errorf("GET /settings/spend cap_usd = %v, want 12.34", got)
	}
}

func TestSpend_ReachedCapRefusesAnEntryPoint(t *testing.T) {
	s := startStack(t, spec{})
	if put := s.do(http.MethodPut, "/settings", map[string]any{"monthly_spend_cap_usd": 0}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings cap 0 = %d %s, want 200", put.Status, put.Body)
	}
	if got := s.get("/settings/spend").Obj(t)["cap_reached"]; got != true {
		t.Fatalf("GET /settings/spend cap_reached = %v, want true for a cap of 0", got)
	}

	acme := repoByName(t, s, "acme/payments-api")
	list := s.get("/repos/" + acme + "/pulls").JSON(t).([]any)
	pr := list[0].(map[string]any)["id"].(string)
	resp := startReview(t, s, pr, map[string]any{"all": true})
	if resp.Status != http.StatusPaymentRequired || resp.errorCode(t) != "spend_cap_reached" {
		t.Errorf("POST /pulls/%s/review with the cap reached = %d %s, want 402 spend_cap_reached", pr, resp.Status, resp.Body)
	}
}

// A ledger entry moves GET /settings/spend and the admission of an entry point, and nothing else does
// (ledger FR8, FR11): the cap is reached by the entries of the month, not by the entries of another
// month or workspace.
func TestSpend_StatusAndAdmissionReadTheLedger(t *testing.T) {
	s := startStack(t, spec{now: capClock})
	ws := workspaceID(t, s)
	requireNoSpend(t, s)
	setCap(t, s, 1)

	other := scalar[string](t, s.db.DB, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	seedLedgerSpend(t, s, other, capNow, 5)
	seedLedgerSpend(t, s, ws, time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC), 5)
	seedLedgerSpend(t, s, ws, capNow, 0.4)
	seedLedgerUnpriced(t, s, ws, capNow)
	if got := s.get("/settings/spend").Obj(t); got["spent_usd"] != 0.4 || got["cap_reached"] != false {
		t.Fatalf("GET /settings/spend below the cap = %v, want spent_usd 0.4 and cap_reached false", got)
	}

	seedLedgerSpend(t, s, ws, capNow, 0.6)
	if got := s.get("/settings/spend").Obj(t); got["spent_usd"] != float64(1) || got["cap_reached"] != true {
		t.Fatalf("GET /settings/spend at the cap = %v, want spent_usd 1 and cap_reached true", got)
	}
	pr := s.get("/repos/" + repoByName(t, s, "acme/payments-api") + "/pulls").JSON(t).([]any)[0].(map[string]any)["id"].(string)
	if r := startReview(t, s, pr, map[string]any{"all": true}); r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Errorf("POST /pulls/%s/review with the ledger at the cap = %d %s, want 402 spend_cap_reached", pr, r.Status, r.Body)
	}
}

func TestAbout_IsWired(t *testing.T) {
	s := startStack(t, spec{})
	resp := s.get("/about")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /about = %d %s, want 200", resp.Status, resp.Body)
	}
	body := resp.Obj(t)
	if body["version"] != "dev" || body["build_id"] != "dev" {
		t.Errorf("GET /about version/build_id = %v/%v, want dev/dev for an unstamped build", body["version"], body["build_id"])
	}
	runner, _ := body["runner"].(map[string]any)
	if runner["kind"] != "go" || runner["version"] != "2" {
		t.Errorf("GET /about runner = %v, want {go 2}", body["runner"])
	}
	if dir, _ := body["data_dir"].(string); dir != s.cfg.DataDir {
		t.Errorf("GET /about data_dir = %q, want %q", body["data_dir"], s.cfg.DataDir)
	}
}
