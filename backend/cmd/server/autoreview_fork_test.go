package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Component test of the fork guard of the automatic-review trigger (Settings C, decision D13,
// security review r1 M1) through the real server: a PR whose head branch is in another repository is
// recorded skipped_fork and never reviewed automatically, a PR of the repository itself starts, and a
// manual review of the fork PR still works.

// arForkPayload makes the GitHub stub name the head and base repositories of every PR it serves
// (the arPull JSON carries none): headRepo maps a PR number to the full name of its head repository;
// a missing number means the repository itself (o/<repo>), the empty string a deleted fork (null).
func (h *arHarness) arForkPayload(headRepo map[int]string) {
	h.t.Helper()
	h.s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		rec := httptest.NewRecorder()
		if !h.gh.handle(rec, r) {
			return false
		}
		var body any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return true
		}
		m := arPullPath.FindStringSubmatch(r.URL.Path)
		base := "o/" + m[1]
		decorate := func(pull map[string]any) {
			own := base
			if full, ok := headRepo[int(pull["number"].(float64))]; ok {
				own = full
			}
			var head any
			if own != "" {
				head = map[string]any{"full_name": own}
			}
			pull["head"].(map[string]any)["repo"] = head
			pull["base"].(map[string]any)["repo"] = map[string]any{"full_name": base}
		}
		switch v := body.(type) {
		case []any:
			for _, p := range v {
				decorate(p.(map[string]any))
			}
		case map[string]any:
			decorate(v)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
		return true
	})
}

// A same-repository PR starts one review; a fork PR and a PR from a deleted fork are recorded
// skipped_fork with no run and no model call; new commits on the fork PR are recorded skipped_fork
// again; a manual review of the fork PR is a normal local run.
func TestAutoReviewFlow_ForkPRsAreSkipped(t *testing.T) { // D13
	h := arStart(t, arOpts{token: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)

	h.arForkPayload(map[int]string{2: "mallory/r", 3: ""})
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: after, updated: now.Add(-3 * time.Minute)}) // the repository's own branch
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: after, updated: now.Add(-2 * time.Minute)}) // a fork
	h.gh.put("r", arPull{number: 3, head: arHead(3, 1), created: after, updated: now.Add(-time.Minute)})     // a deleted fork
	h.addRepo("r")

	h.waitRecords(3)
	h.requireRecords(
		arWant(1, 1, "new_pr", "started", 1),
		arWant(2, 1, "new_pr", "skipped_fork", 0),
		arWant(3, 1, "new_pr", "skipped_fork", 0))
	h.runsDone()
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs = %d, want 1 (the same-repository PR only)", got)
	}
	for _, number := range []int{2, 3} {
		if got := count(t, h.db, `SELECT count(*) FROM agent_runs WHERE pr_id = ?`, h.pullID(number)); got != 0 {
			t.Errorf("runs on fork PR %d = %d, want 0", number, got)
		}
	}
	calls := h.llm.calls()

	// New commits on the fork PR: decided once more, skipped again.
	h.gh.edit(t, "r", 2, func(p *arPull) { p.head, p.updated = arHead(2, 2), h.clock.Now() })
	h.cycle(time.Minute)
	h.waitRecords(4)
	h.settle()
	h.requireRecords(
		arWant(1, 1, "new_pr", "started", 1),
		arWant(2, 1, "new_pr", "skipped_fork", 0),
		arWant(2, 2, "new_commits", "skipped_fork", 0),
		arWant(3, 1, "new_pr", "skipped_fork", 0))
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs after new commits on the fork PR = %d, want 1", got)
	}
	if got := h.llm.calls(); got != calls {
		t.Errorf("model calls grew from %d to %d, want no call for a fork PR", calls, got)
	}

	// A manual review of the fork PR is not affected: a local run, and no new record.
	resp := startReview(t, h.s, h.pullID(2), map[string]any{"agentId": a1})
	if resp.Status != http.StatusOK {
		t.Fatalf("manual review of the fork PR = %d %s, want 200", resp.Status, resp.Body)
	}
	h.runsDone()
	if got := count(t, h.db, `SELECT count(*) FROM agent_runs WHERE pr_id = ? AND source = 'local'`, h.pullID(2)); got != 1 {
		t.Errorf("local runs on the fork PR = %d, want 1", got)
	}
	if got := count(t, h.db, `SELECT count(*) FROM auto_review_records`); got != 4 {
		t.Errorf("records after the manual review = %d, want 4 (unchanged)", got)
	}
	h.requireDecisionLogs()
}

// A PR stored before the head repository was recorded (NULL) is neither started nor recorded until a
// poll shows it; then it is decided like any other PR.
func TestAutoReviewFlow_UnknownHeadRepositoryWaitsForThePoll(t *testing.T) { // D13
	h := arStart(t, arOpts{token: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)

	// PR 1 is stored without a head repository and the stub does not list it: the poll cannot refresh it.
	h.arForkPayload(nil)
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: after, updated: now.Add(-time.Minute)})
	h.addRepo("r", func(tx *gorm.DB, repoID string) {
		id := h.insertPR(tx, repoID, arPull{number: 1, head: arHead(1, 1), created: after, updated: now.Add(-2 * time.Minute)}, "")
		if err := tx.Exec(`UPDATE pull_requests SET head_is_fork = NULL WHERE id = ?`, id).Error; err != nil {
			t.Fatal(err)
		}
	})

	h.waitRecords(1)
	h.requireRecords(arWant(2, 1, "new_pr", "started", 1))
	h.runsDone()
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(2, 1, "new_pr", "started", 1))
	if got := count(t, h.db, `SELECT count(*) FROM agent_runs WHERE pr_id = ?`, h.pullID(1)); got != 0 {
		t.Errorf("runs on the PR with an unknown head repository = %d, want 0", got)
	}

	// The stub now lists PR 1 (its own branch): the poll stores the head repository and the next
	// evaluation starts it.
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: after, updated: h.clock.Now()})
	h.cycle(time.Minute)
	h.waitRecords(2)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 1), arWant(2, 1, "new_pr", "started", 1))
	h.runsDone()
}
