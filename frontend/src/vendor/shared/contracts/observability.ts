import { z } from 'zod';
import { Severity } from './findings.js';
import { FindingRecord } from './review-api.js';

/**
 * A5 — Observability / Multi-agent contracts (L07, specs/multi-agent-review.md).
 *
 * These are NEW contracts (A5 owns this file; the barrel re-exports it). They
 * sit alongside A2's `review-api.ts`:
 *   - MultiAgentRun        GET /multi-agent-runs/:id (parent of N agent runs,
 *                          started by POST /pulls/:id/review with 2+ agentIds)
 *   - MultiAgentRunSummary GET /repos/:id/multi-agent-runs (newest first)
 *   - AgentRunEstimate     GET /runs/estimates (Configure run screen)
 *   - AgentColumn          one agent's column in the multi-agent view
 *   - Conflict / ConflictTake  one location group and each agent's take on it
 *   - AgentStats           per-agent quality aggregates (GET /agents/:id/stats)
 *   - CuratorResult        the cross-session memory curator outcome
 *
 * The single-document run trace itself stays in `contracts/trace.ts` (RunTrace).
 */

// ---------------------------------------------------------------------------
// Multi-Agent Review
// ---------------------------------------------------------------------------

/** A finding as surfaced in a multi-agent column (subset of FindingRecord). */
export const AgentColumnFinding = z.object({
  id: z.string(),
  severity: Severity,
  category: z.string(),
  title: z.string(),
  file: z.string(),
  start_line: z.number().int(),
  kind: z.string().nullish(),
});
export type AgentColumnFinding = z.infer<typeof AgentColumnFinding>;

/** One agent's result column in the multi-agent review. */
export const AgentColumn = z.object({
  run_id: z.string(),
  agent_id: z.string(),
  /** Stored name; the client labels a deleted agent. */
  agent_name: z.string(),
  provider: z.string().nullable(),
  model: z.string().nullable(),
  status: z.enum(['done', 'failed', 'running', 'cancelled']),
  error: z.string().nullish(),
  verdict: z.string().nullable(),
  score: z.number().int().nullable(),
  summary: z.string().nullable(),
  duration_ms: z.number().int().nullable(),
  cost_usd: z.number().nullable(),
  findings: z.array(FindingRecord),
});
export type AgentColumn = z.infer<typeof AgentColumn>;

/** One agent's stance on a location group. */
export const ConflictTake = z.object({
  agent_id: z.string(),
  /** Agent name. */
  persona: z.string(),
  run_id: z.string(),
  /** Highest severity the agent flagged in the group, or 'ignored' = "did not flag". */
  verdict: z.union([Severity, z.literal('ignored')]),
  /** Title of the agent's highest-severity finding in the group; "" when ignored. */
  note: z.string(),
  /** The agent's findings in the group; empty when ignored. */
  finding_ids: z.array(z.string()),
});
export type ConflictTake = z.infer<typeof ConflictTake>;

/**
 * A location group = findings in the same file whose line ranges overlap
 * (chains merge). `is_conflict` is false when every finished agent flagged it
 * with the same severity. Computed from persisted findings on read; not stored.
 */
export const Conflict = z.object({
  file: z.string(),
  /** Min start_line of the group. */
  line: z.number().int(),
  /** Max end_line of the group. */
  end_line: z.number().int(),
  title: z.string(),
  is_conflict: z.boolean(),
  takes: z.array(ConflictTake),
});
export type Conflict = z.infer<typeof Conflict>;

/** Response of GET /multi-agent-runs/:id. Totals and status are derived on read. */
export const MultiAgentRun = z.object({
  id: z.string(),
  pr_id: z.string(),
  repo_id: z.string(),
  pr_number: z.number().int().nullish(),
  pr_title: z.string().nullish(),
  ran_at: z.string(),
  agent_count: z.number().int(),
  /** Wall clock of a parallel fan-out: max column duration_ms (0 when none). */
  total_duration_ms: z.number().int(),
  /** Sum of non-null column costs; null when all are null. */
  total_cost_usd: z.number().nullable(),
  columns: z.array(AgentColumn),
  /** Every location group, conflict or not. */
  conflicts: z.array(Conflict),
});
export type MultiAgentRun = z.infer<typeof MultiAgentRun>;

/** Row of GET /repos/:id/multi-agent-runs (newest ran_at first). */
export const MultiAgentRunSummary = z.object({
  id: z.string(),
  pr_id: z.string(),
  pr_number: z.number().int().nullable(),
  pr_title: z.string().nullable(),
  ran_at: z.string(),
  agent_count: z.number().int(),
  running_count: z.number().int(),
  total_cost_usd: z.number().nullable(),
});
export type MultiAgentRunSummary = z.infer<typeof MultiAgentRunSummary>;

/** Per-agent averages over its last ≤ 10 done runs (GET /runs/estimates). */
export const AgentRunEstimate = z.object({
  agent_id: z.string(),
  sample_size: z.number().int(),
  avg_duration_ms: z.number().nullable(),
  avg_cost_usd: z.number().nullable(),
});
export type AgentRunEstimate = z.infer<typeof AgentRunEstimate>;

// ---------------------------------------------------------------------------
// Per-agent Stats (GET /agents/:id/stats)
// ---------------------------------------------------------------------------

/** A single (date, value) point for a sparkline/trend. */
export const StatPoint = z.object({ label: z.string(), value: z.number() });
export type StatPoint = z.infer<typeof StatPoint>;

/** The resolved reporting window echoed by the stats/performance endpoints. */
export const PeriodInfo = z.object({
  key: z.enum(["1d", "30d", "custom"]),
  from: z.string(),
  to: z.string(),
});
export type PeriodInfo = z.infer<typeof PeriodInfo>;

/**
 * Per-agent metrics over the counted runs of a period (done/failed/cancelled,
 * source local/ci). Returned identically by GET /agents/:id/stats and as a row
 * of GET /agents/performance. Costs are DevDigest estimates, rounded to 6 dp.
 * Invariants: runs = runs_done + runs_failed + runs_cancelled = runs_local +
 * runs_ci = priced_runs + unpriced_runs; findings_total = accepted + dismissed
 * + pending.
 */
export const AgentPeriodMetrics = z.object({
  runs: z.number().int(),
  runs_done: z.number().int(),
  runs_failed: z.number().int(),
  runs_cancelled: z.number().int(),
  runs_local: z.number().int(),
  runs_ci: z.number().int(),
  priced_runs: z.number().int(),
  unpriced_runs: z.number().int(),
  total_cost_usd: z.number().nullable(),
  avg_cost_usd: z.number().nullable(),
  avg_duration_ms: z.number().int().nullable(),
  last_run_at: z.string().nullable(),
  findings_total: z.number().int(),
  accepted: z.number().int(),
  dismissed: z.number().int(),
  pending: z.number().int(),
  decisions: z.number().int(),
  /** accepted / decisions, 0..1; null when decisions = 0. */
  accept_rate: z.number().min(0).max(1).nullable(),
  /** true when 0 < decisions < min_decisions. */
  small_sample: z.boolean(),
});
export type AgentPeriodMetrics = z.infer<typeof AgentPeriodMetrics>;

/**
 * A cost-breakdown segment. `key` is the agent id / model id (null = deleted
 * agents / unknown model); `label` is the agent name / model id (null likewise,
 * the client supplies the wording). `runs` = priced runs in the segment.
 */
export const CostSegment = z.object({
  key: z.string().nullable(),
  label: z.string().nullable(),
  value: z.number().positive(),
  runs: z.number().int(),
});
export type CostSegment = z.infer<typeof CostSegment>;

/** A skill / memory that was part of an agent's counted runs; `runs` = counted runs it was in. */
export const UsageCount = z.object({
  key: z.string(),
  label: z.string(),
  runs: z.number().int(),
});
export type UsageCount = z.infer<typeof UsageCount>;

/** Findings of the counted runs whose run started in one equal-width slice of the period. */
export const SeverityBucket = z.object({
  /** Slice start (ISO). */
  from: z.string(),
  critical: z.number().int(),
  warning: z.number().int(),
  suggestion: z.number().int(),
});
export type SeverityBucket = z.infer<typeof SeverityBucket>;

export const CategoryCount = z.object({
  category: z.string(),
  count: z.number().int(),
});
export type CategoryCount = z.infer<typeof CategoryCount>;

/** One counted run of the agent (Stats tab run history), newest first. */
export const RunHistoryItem = z.object({
  run_id: z.string(),
  ran_at: z.string(),
  status: z.enum(["done", "failed", "cancelled"]),
  source: z.enum(["local", "ci", "auto"]),
  /** The PR the run reviewed; null when the PR was deleted or the run had none. */
  pr: z.object({ id: z.string(), repo_id: z.string(), number: z.number().int() }).nullable(),
  /** tokens_in + tokens_out; null when no usage was recorded (CI runs). */
  tokens: z.number().int().nullable(),
  cost_usd: z.number().nullable(),
  findings_count: z.number().int().nullable(),
  /** Whether a persisted run trace exists (CI runs have none). */
  has_trace: z.boolean(),
});
export type RunHistoryItem = z.infer<typeof RunHistoryItem>;

/** Response of GET /agents/:id/stats. */
export const AgentStats = z.object({
  agent_id: z.string(),
  agent_name: z.string(),
  period: PeriodInfo,
  min_decisions: z.number().int(),
  cost_basis: z.literal("estimate"),
  metrics: AgentPeriodMetrics,
  /** The same metrics over the equally long window right before the period (deltas, trend arrows). */
  previous: AgentPeriodMetrics,
  /** Counted runs per equal-width slice of the period, oldest first; `label` = slice start (ISO). */
  runs_trend: z.array(StatPoint),
  cost_by_model: z.array(CostSegment),
  /** Top skills by counted runs that had them in the prompt, most runs first. */
  skills_used: z.array(UsageCount),
  /** Top memories by counted runs that pulled them (run trace), most runs first. */
  memory_pulled: z.array(UsageCount),
  findings_by_severity: z.array(SeverityBucket),
  /** Non-zero categories, most findings first. */
  findings_by_category: z.array(CategoryCount),
  /** The newest counted runs of the period (at most 20), newest first. */
  run_history: z.array(RunHistoryItem),
});
export type AgentStats = z.infer<typeof AgentStats>;

// ---------------------------------------------------------------------------
// Cross-session memory curator
// ---------------------------------------------------------------------------

/** A merge the curator performed (or would perform in dry-run). */
export const CuratorMerge = z.object({
  kept_id: z.string(),
  merged_ids: z.array(z.string()),
  content: z.string(),
  similarity: z.number(),
});
export type CuratorMerge = z.infer<typeof CuratorMerge>;

export const CuratorResult = z.object({
  scanned: z.number().int(),
  merges: z.array(CuratorMerge),
  removed: z.number().int(),
  dry_run: z.boolean(),
});
export type CuratorResult = z.infer<typeof CuratorResult>;
