/* test/eval-fixtures.ts — contract-shaped eval pipeline rows (cases, runs, case
   results) for the eval UI tests. */
import type { AgentEvalCase, EvalCaseResult, EvalCompare, EvalPromptDiffLine, EvalRunSummary } from "@devdigest/shared";

export function makeRun(overrides: Partial<EvalRunSummary> = {}): EvalRunSummary {
  return {
    id: "run1",
    owner_kind: "agent",
    skill_id: null,
    skill_version: null,
    agent_id: "ag1",
    agent_version: 3,
    agent_name: "Security Reviewer",
    model: "mock/reviewer",
    status: "completed",
    started_at: "2026-10-14T10:00:00.000Z",
    finished_at: "2026-10-14T10:01:00.000Z",
    cases_total: 8,
    cases_finished: 8,
    cases_passed: 6,
    cases_errored: 0,
    recall: 0.75,
    precision: 0.8,
    citation_accuracy: 0.9,
    unlabeled_findings: 1,
    cost_usd: 0.0123,
    duration_ms: 60_000,
    error: null,
    ...overrides,
  };
}

export function makeCase(overrides: Partial<AgentEvalCase> = {}): AgentEvalCase {
  return {
    id: "case1",
    owner_kind: "agent",
    owner_id: "ag1",
    agent_id: "ag1",
    name: "stripe-key-leak",
    notes: null,
    expectation: {
      kind: "must_find",
      file: "src/config.ts",
      start_line: 10,
      end_line: 12,
      severity: "critical",
      category: "security",
      title: "Hard-coded Stripe key",
    },
    input_diff: "diff --git a/src/config.ts b/src/config.ts\n--- a/src/config.ts\n+++ b/src/config.ts\n@@ -10,2 +10,3 @@\n+  stripeKey: \"sk_live_x\"\n",
    input_meta: { pr_title: "Add Stripe", pr_description: "Wire payments", repo: "acme/shop", pr_number: 7, head_sha: "abc1234" },
    source_finding_id: "f1",
    source_pr_id: "pr1",
    origin: "finding",
    content_edited_at: null,
    created_at: "2026-10-14T09:00:00.000Z",
    last_result: null,
    ...overrides,
  };
}

export function makeCaseResult(overrides: Partial<EvalCaseResult> = {}): EvalCaseResult {
  const base = makeCase();
  return {
    case_id: base.id,
    case_name: base.name,
    expectation: base.expectation,
    status: "passed",
    findings: [],
    dropped_count: 0,
    duration_ms: 1000,
    cost_usd: 0.001,
    error: null,
    ...overrides,
  };
}

/** Runs A (v2, older) → B (v3): recall +25pt, precision -5pt; one of three cases flipped; one prompt line replaced. */
export function makeCompare({ prompt }: { prompt?: EvalPromptDiffLine[] } = {}): EvalCompare {
  const a = makeRun({ id: "ra", agent_version: 2, started_at: "2026-10-13T10:00:00.000Z", recall: 0.5, precision: 0.9, citation_accuracy: 0.9, cost_usd: 0.01 });
  const b = makeRun({ id: "rb", agent_version: 3, recall: 0.75, precision: 0.85, citation_accuracy: 0.9, cost_usd: 0.012 });
  return {
    a,
    b,
    delta: { recall: 0.25, precision: -0.05, citation_accuracy: 0, cost_usd: 0.002 },
    cases: [
      { case_id: "c1", case_name: "stripe-key-leak", a: "failed", b: "passed", flipped: true },
      { case_id: "c2", case_name: "sql-injection", a: "passed", b: "passed", flipped: false },
      { case_id: "c3", case_name: "new-case", a: "absent", b: "passed", flipped: false },
    ],
    config_diff: {
      system_prompt: prompt ?? [
        { op: "same", line: "You are a security reviewer." },
        { op: "del", line: "Flag every issue." },
        { op: "add", line: "Flag only high-confidence issues." },
      ],
      model: null,
      strategy: null,
      skills_added: [],
      skills_removed: [],
    },
  };
}
