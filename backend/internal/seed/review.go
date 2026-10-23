package seed

import (
	"context"
	"fmt"
)

// The PR #482 demo review: one review with two findings and the finished run behind it,
// verbatim from the dev-digest Node seed (db/seed.ts:148-189 and :317-355, spec section 7.13).
const (
	demoAgentName     = "General Reviewer"
	demoReviewModel   = "seed"
	demoRunGrounding  = "2/2 passed"
	demoRunStatus     = "done"
	demoRunSource     = "local"
	demoRunDurationMs = 18_420
	demoRunTokensIn   = 6_214
	demoRunTokensOut  = 842
	demoRunCostUSD    = 0.002135
	demoReviewScore   = 61
	demoRunBlockers   = 1
)

// DemoReview is the demo review of one pull request with its findings and its finished run.
// AgentID is empty when the workspace has no agent named "General Reviewer"; the store then
// leaves reviews.agent_id and agent_runs.agent_id null. The run's ran_at is the review's
// created_at, and reviews.run_id points at the run.
type DemoReview struct {
	WorkspaceID string
	PRID        string
	AgentID     string
	Kind        string
	Verdict     string
	Summary     string
	Score       int
	Model       string
	Findings    []DemoFinding
	Run         DemoRun
}

// DemoFinding is one finding of the demo review (kind stays the column default "finding").
type DemoFinding struct {
	File       string
	StartLine  int
	EndLine    int
	Severity   string
	Category   string
	Title      string
	Rationale  string
	Suggestion string
	Confidence float64
	// Accepted and Dismissed set findings.accepted_at / dismissed_at to the review's
	// created_at; both false leaves the finding pending.
	Accepted  bool
	Dismissed bool
}

// DemoRun is the finished agent run behind the demo review.
type DemoRun struct {
	Provider      string
	Model         string
	DurationMs    int
	TokensIn      int
	TokensOut     int
	CostUSD       float64
	Status        string
	Source        string
	FindingsCount int
	Grounding     string
	Score         int
	Blockers      int
}

// demoReview builds the PR #482 demo review. The values are fixed; only the ids vary.
func demoReview(workspaceID, prID, agentID string) DemoReview {
	return DemoReview{
		WorkspaceID: workspaceID,
		PRID:        prID,
		AgentID:     agentID,
		Kind:        "review",
		Verdict:     "request_changes",
		Summary:     "Solid middleware approach, but a Stripe secret key is committed in plaintext and the user-list endpoint introduces an N+1 query under the new limiter.",
		Score:       demoReviewScore,
		Model:       demoReviewModel,
		Findings: []DemoFinding{
			{
				File: "src/config.ts", StartLine: 12, EndLine: 12,
				Severity: "CRITICAL", Category: "security",
				Title:      "Hardcoded Stripe secret key in commit",
				Rationale:  "Line 12 contains a literal `sk_live_` Stripe secret key.",
				Suggestion: "Move to env var and rotate the key immediately.",
				Confidence: 0.98,
			},
			{
				File: "src/api/users.ts", StartLine: 45, EndLine: 52,
				Severity: "WARNING", Category: "perf",
				Title:      "N+1 query in user list endpoint",
				Rationale:  "Loop issues one query per user → N+1.",
				Suggestion: "Use a single IN query and group in memory.",
				Confidence: 0.86,
			},
		},
		Run: DemoRun{
			Provider:      seedAgentProvider,
			Model:         seedAgentModel,
			DurationMs:    demoRunDurationMs,
			TokensIn:      demoRunTokensIn,
			TokensOut:     demoRunTokensOut,
			CostUSD:       demoRunCostUSD,
			Status:        demoRunStatus,
			Source:        demoRunSource,
			FindingsCount: 2,
			Grounding:     demoRunGrounding,
			Score:         demoReviewScore,
			Blockers:      demoRunBlockers,
		},
	}
}

// writeDemoReview seeds the PR #482 demo review (FR4044, D4015) unless the pull request already
// has a review or an agent run: a restart never re-creates a demo review the user deleted, and
// never adds one next to a run the user made. Deleting the demo run deletes its review, so the
// next start writes both again. It runs after the agents so "General Reviewer" can be linked.
func (s *Seeder) writeDemoReview(ctx context.Context, tx Tx, workspaceID, prID string, res *Result) error {
	used, err := tx.PullRequestHasReviewOrRun(ctx, prID)
	if err != nil {
		return fmt.Errorf("check demo review: %w", err)
	}
	if used {
		return nil
	}
	agent, _, err := tx.FindAgent(ctx, workspaceID, demoAgentName)
	if err != nil {
		return fmt.Errorf("find %s: %w", demoAgentName, err)
	}
	d := demoReview(workspaceID, prID, agent.ID)
	if err := tx.InsertDemoReview(ctx, d); err != nil {
		return fmt.Errorf("demo review: %w", err)
	}
	res.Reviews++
	res.Findings += len(d.Findings)
	res.AgentRuns++
	return nil
}
