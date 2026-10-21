import { z } from 'zod';
import {
  EVAL_CASE_NAME_MAX,
  EVAL_CASES_PER_AGENT_MAX,
  EVAL_DIFF_FILES_DETAIL_MAX,
  EVAL_EXPECTATION_FILE_MAX,
  EVAL_EXPECTATION_TITLE_MAX,
  EVAL_INPUT_DIFF_MAX_CHARS,
  EVAL_PERIOD_TREND_MAX,
  EVAL_PERIODS,
  EVAL_PR_DESCRIPTION_MAX,
  EVAL_PR_TITLE_MAX,
} from '../constants/evals.js';
import { FindingCategory, Severity } from './findings.js';
import { EvalOwnerKind } from './knowledge.js'; // who owns a case / a suite run (also eval-ci)

/**
 * Eval pipeline contracts (specs/eval-pipeline.md §7): turn a decided finding into an
 * agent-owned eval case, run an agent's case set in the background with code-only
 * metrics, then list / compare / dashboard the runs.
 *
 * ADDITIVE next to the legacy `EvalRun` / `EvalCase` / `EvalRunRecord` / `EvalDashboard`
 * contracts (knowledge.ts, eval-ci.ts), which stay unchanged — the names here are
 * deliberately distinct (`AgentEvalCase`, `EvalRunSummary`, …).
 */

// ---- limits (defined zod-free in constants/evals.ts; the client imports that subpath) ----
export {
  EVAL_CASE_NAME_MAX,
  EVAL_CASES_PER_AGENT_MAX,
  EVAL_DIFF_FILES_DETAIL_MAX,
  EVAL_EXPECTATION_FILE_MAX,
  EVAL_EXPECTATION_TITLE_MAX,
  EVAL_INPUT_DIFF_MAX_CHARS,
  EVAL_PR_DESCRIPTION_MAX,
  EVAL_PR_TITLE_MAX,
};

// ---- enums ----
export const EvalExpectationKind = z.enum(['must_find', 'must_not_flag']);
export type EvalExpectationKind = z.infer<typeof EvalExpectationKind>;

/** Per-case outcome. */
export const EvalCaseStatus = z.enum(['passed', 'failed', 'error']);
export type EvalCaseStatus = z.infer<typeof EvalCaseStatus>;

/** Status of one execution of an agent's whole case set (a "suite run"). */
export const EvalRunStatus = z.enum(['running', 'completed', 'failed']);
export type EvalRunStatus = z.infer<typeof EvalRunStatus>;

export const EvalFindingMatch = z.enum(['hit', 'violation', 'unlabeled']);
export type EvalFindingMatch = z.infer<typeof EvalFindingMatch>;

/** Derived: `finding` when the case has a source finding, else `manual`. */
export const EvalCaseOrigin = z.enum(['finding', 'manual']);
export type EvalCaseOrigin = z.infer<typeof EvalCaseOrigin>;

export const EvalMetricName = z.enum(['recall', 'precision', 'citation_accuracy']);
export type EvalMetricName = z.infer<typeof EvalMetricName>;

/** Error `code` values of the eval routes (lower snake_case, like the rest of the envelope). */
export const EvalErrorCode = z.enum([
  'not_found',
  'validation_error',
  'eval_case_exists',
  'finding_undecided',
  'finding_no_agent',
  'patch_unavailable',
  'patch_too_large',
  'eval_case_limit',
  'eval_run_active',
  'no_eval_cases',
  'runs_not_comparable',
  'diff_invalid',
  'expectation_file_not_in_diff',
  'expectation_lines_not_in_diff',
  'host_agent_not_found',
]);
export type EvalErrorCode = z.infer<typeof EvalErrorCode>;

// ---- eval case ----
export const EvalExpectation = z
  .object({
    kind: EvalExpectationKind,
    file: z.string(),
    start_line: z.number().int().min(1),
    end_line: z.number().int().min(1),
    severity: z.string().nullable(),
    category: z.string().nullable(),
    title: z.string().nullable(),
  })
  .refine((e) => e.end_line >= e.start_line, {
    message: 'end_line must be >= start_line',
    path: ['end_line'],
  });
export type EvalExpectation = z.infer<typeof EvalExpectation>;

export const EvalCaseInputMeta = z.object({
  pr_title: z.string(),
  pr_description: z.string().nullable(),
  /** Null on a manual case. */
  repo: z.string().nullable(),
  pr_number: z.number().int().nullable(),
  head_sha: z.string().nullable(),
});
export type EvalCaseInputMeta = z.infer<typeof EvalCaseInputMeta>;

export const EvalCaseLastResult = z.object({
  run_id: z.string(),
  status: EvalCaseStatus,
  ran_at: z.string(),
});
export type EvalCaseLastResult = z.infer<typeof EvalCaseLastResult>;

export const AgentEvalCase = z.object({
  id: z.string(),
  owner_kind: EvalOwnerKind,
  owner_id: z.string(),
  /** The owning agent; null for a skill case. */
  agent_id: z.string().nullable(),
  name: z.string().min(1).max(EVAL_CASE_NAME_MAX),
  notes: z.string().nullable(),
  expectation: EvalExpectation,
  input_diff: z.string(),
  input_meta: EvalCaseInputMeta,
  source_finding_id: z.string().nullable(),
  source_pr_id: z.string().nullable(),
  origin: EvalCaseOrigin,
  /** Set at the first edit that changes the case's content; never cleared. */
  content_edited_at: z.string().nullable(),
  created_at: z.string(),
  last_result: EvalCaseLastResult.nullable(),
});
export type AgentEvalCase = z.infer<typeof AgentEvalCase>;

/** `PATCH /eval-cases/:id` — field shape only; the merged values are checked by the service (end >= start → 400). */
export const AgentEvalCaseUpdateInput = z.object({
  name: z.string().trim().min(1).max(EVAL_CASE_NAME_MAX).optional(),
  notes: z.string().nullable().optional(),
  kind: EvalExpectationKind.optional(),
  file: z.string().min(1).max(EVAL_EXPECTATION_FILE_MAX).optional(),
  start_line: z.number().int().min(1).optional(),
  end_line: z.number().int().min(1).optional(),
  severity: Severity.nullish(),
  category: FindingCategory.nullish(),
  title: z.string().max(EVAL_EXPECTATION_TITLE_MAX).nullish(),
  input_diff: z.string().min(1).optional(),
  pr_title: z.string().max(EVAL_PR_TITLE_MAX).nullish(),
  pr_description: z.string().max(EVAL_PR_DESCRIPTION_MAX).nullish(),
});
export type AgentEvalCaseUpdateInput = z.infer<typeof AgentEvalCaseUpdateInput>;

/**
 * `POST /agents/:id/eval-cases` and `POST /skills/:id/eval-cases`. `input_diff` has no
 * schema-level upper bound: the service rejects a STORED diff over EVAL_INPUT_DIFF_MAX_CHARS
 * as `patch_too_large` (422), after wrapping a header-less paste.
 */
export const EvalCaseCreateInput = z.object({
  name: z.string().trim().min(1).max(EVAL_CASE_NAME_MAX),
  notes: z.string().nullish(),
  expectation: z
    .object({
      kind: EvalExpectationKind,
      file: z.string().min(1).max(EVAL_EXPECTATION_FILE_MAX),
      start_line: z.number().int().min(1),
      end_line: z.number().int().min(1),
      severity: Severity.nullish(),
      category: FindingCategory.nullish(),
      title: z.string().max(EVAL_EXPECTATION_TITLE_MAX).nullish(),
    })
    .refine((e) => e.end_line >= e.start_line, {
      message: 'end_line must be >= start_line',
      path: ['end_line'],
    }),
  input_diff: z.string().min(1),
  input_meta: z
    .object({
      pr_title: z.string().max(EVAL_PR_TITLE_MAX).nullish(),
      pr_description: z.string().max(EVAL_PR_DESCRIPTION_MAX).nullish(),
    })
    .optional(),
});
export type EvalCaseCreateInput = z.infer<typeof EvalCaseCreateInput>;

export const AgentEvalCaseList = z.object({ cases: z.array(AgentEvalCase) });
export type AgentEvalCaseList = z.infer<typeof AgentEvalCaseList>;

// ---- runs ----
/** Metrics are null while running and when their denominator is 0. */
export const EvalRunSummary = z.object({
  id: z.string(),
  owner_kind: EvalOwnerKind,
  /** The skill a skill run tests; null for an agent run. */
  skill_id: z.string().nullable(),
  skill_version: z.number().int().nullable(),
  /** For a skill run the three fields below describe the HOST agent. */
  agent_id: z.string(),
  agent_version: z.number().int(),
  /** Agent name frozen at run start. */
  agent_name: z.string(),
  model: z.string(),
  status: EvalRunStatus,
  started_at: z.string(),
  finished_at: z.string().nullable(),
  cases_total: z.number().int(),
  cases_finished: z.number().int(),
  cases_passed: z.number().int(),
  cases_errored: z.number().int(),
  recall: z.number().min(0).max(1).nullable(),
  precision: z.number().min(0).max(1).nullable(),
  citation_accuracy: z.number().min(0).max(1).nullable(),
  unlabeled_findings: z.number().int(),
  cost_usd: z.number().nullable(),
  duration_ms: z.number().int().nullable(),
  error: z.string().nullable(),
});
export type EvalRunSummary = z.infer<typeof EvalRunSummary>;

export const EvalResultFinding = z.object({
  file: z.string(),
  start_line: z.number().int(),
  end_line: z.number().int(),
  severity: z.string().nullable(),
  category: z.string().nullable(),
  title: z.string().nullable(),
  match: EvalFindingMatch,
});
export type EvalResultFinding = z.infer<typeof EvalResultFinding>;

export const EvalCaseResult = z.object({
  case_id: z.string(),
  case_name: z.string(),
  expectation: EvalExpectation,
  status: EvalCaseStatus,
  findings: z.array(EvalResultFinding),
  dropped_count: z.number().int(),
  duration_ms: z.number().int(),
  cost_usd: z.number().nullable(),
  error: z.string().nullable(),
});
export type EvalCaseResult = z.infer<typeof EvalCaseResult>;

/** `POST /skills/:id/eval-runs` — the host agent the skill's cases run on. */
export const EvalSkillRunStartInput = z.object({ agent_id: z.string().uuid() });
export type EvalSkillRunStartInput = z.infer<typeof EvalSkillRunStartInput>;

export const EvalRunList = z.object({ runs: z.array(EvalRunSummary) });
export type EvalRunList = z.infer<typeof EvalRunList>;

export { EVAL_PERIOD_TREND_MAX, EVAL_PERIODS };

/** Dashboard / runs-list window: runs started within the last N days of server time, or all of them. */
export const EvalPeriod = z.enum(EVAL_PERIODS);
export type EvalPeriod = z.infer<typeof EvalPeriod>;

export const EvalRunListQuery = z.object({
  limit: z.coerce.number().int().min(1).max(100).default(20),
  period: EvalPeriod.optional(),
});
export type EvalRunListQuery = z.infer<typeof EvalRunListQuery>;

export const EvalRunDetail = z.object({
  run: EvalRunSummary,
  results: z.array(EvalCaseResult),
});
export type EvalRunDetail = z.infer<typeof EvalRunDetail>;

// ---- compare ----
export const EvalCompareQuery = z.object({ a: z.string().min(1), b: z.string().min(1) });
export type EvalCompareQuery = z.infer<typeof EvalCompareQuery>;

export const EvalCompareCaseStatus = z.union([EvalCaseStatus, z.literal('absent')]);
export type EvalCompareCaseStatus = z.infer<typeof EvalCompareCaseStatus>;

export const EvalPromptDiffLine = z.object({
  op: z.enum(['same', 'add', 'del']),
  line: z.string(),
});
export type EvalPromptDiffLine = z.infer<typeof EvalPromptDiffLine>;

const EvalValueChange = z.object({ a: z.string(), b: z.string() });

export const EvalCompare = z.object({
  a: EvalRunSummary,
  b: EvalRunSummary,
  delta: z.object({
    recall: z.number().nullable(),
    precision: z.number().nullable(),
    citation_accuracy: z.number().nullable(),
    cost_usd: z.number().nullable(),
  }),
  cases: z.array(
    z.object({
      case_id: z.string(),
      case_name: z.string(),
      a: EvalCompareCaseStatus,
      b: EvalCompareCaseStatus,
      flipped: z.boolean(),
    }),
  ),
  config_diff: z.object({
    system_prompt: z.array(EvalPromptDiffLine),
    model: EvalValueChange.nullable(),
    strategy: EvalValueChange.nullable(),
    skills_added: z.array(z.string()),
    skills_removed: z.array(z.string()),
  }),
});
export type EvalCompare = z.infer<typeof EvalCompare>;

// ---- dashboards ----
export const EvalSparkPoint = z.object({
  run_id: z.string(),
  recall: z.number().nullable(),
  precision: z.number().nullable(),
  citation_accuracy: z.number().nullable(),
});
export type EvalSparkPoint = z.infer<typeof EvalSparkPoint>;

export const EvalAgentOverview = z.object({
  agent_id: z.string(),
  name: z.string(),
  model: z.string(),
  cases_total: z.number().int(),
  latest: EvalRunSummary.nullable(),
  sparkline: z.array(EvalSparkPoint).max(10),
});
export type EvalAgentOverview = z.infer<typeof EvalAgentOverview>;

export const EvalRecentRun = EvalRunSummary.extend({ agent_name: z.string() });
export type EvalRecentRun = z.infer<typeof EvalRecentRun>;

export const EvalOverview = z.object({
  agents: z.array(EvalAgentOverview),
  recent_runs: z.array(EvalRecentRun).max(20),
});
export type EvalOverview = z.infer<typeof EvalOverview>;

export const EvalRegressionAlert = z.object({
  metrics: z.array(z.object({ metric: EvalMetricName, delta: z.number() })),
});
export type EvalRegressionAlert = z.infer<typeof EvalRegressionAlert>;

export const EvalAgentDashboardQuery = z.object({ period: EvalPeriod.optional() });
export type EvalAgentDashboardQuery = z.infer<typeof EvalAgentDashboardQuery>;

export const EvalAgentDashboard = z.object({
  agent_id: z.string(),
  cases_total: z.number().int(),
  latest: EvalRunSummary.nullable(),
  previous: EvalRunSummary.nullable(),
  trend: z.array(EvalRunSummary).max(EVAL_PERIOD_TREND_MAX),
  alert: EvalRegressionAlert.nullable(),
});
export type EvalAgentDashboard = z.infer<typeof EvalAgentDashboard>;

// ---- run-all ----
export const EvalRunAllSkipReason = z.enum(['no_cases', 'run_active']);
export type EvalRunAllSkipReason = z.infer<typeof EvalRunAllSkipReason>;

export const EvalRunAllResponse = z.object({
  started: z.array(EvalRunSummary),
  skipped: z.array(z.object({ agent_id: z.string(), reason: EvalRunAllSkipReason })),
});
export type EvalRunAllResponse = z.infer<typeof EvalRunAllResponse>;
