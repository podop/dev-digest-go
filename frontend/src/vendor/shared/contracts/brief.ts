import { z } from 'zod';

/**
 * PR Brief building blocks: Intent, Blast radius, Risks, PR History,
 * Smart Diff. Composed into PrBrief.
 */

// ---- Intent ----
/** How sure the intent layer is about the derived intent (server/specs/05-intent-layer.md). */
export const IntentConfidence = z.enum(['high', 'medium', 'low']);
export type IntentConfidence = z.infer<typeof IntentConfidence>;

/** Whether the intent came from real PR text (title/body/ticket/doc) or was guessed. */
export const IntentDerivedFrom = z.enum(['explicit', 'inferred']);
export type IntentDerivedFrom = z.infer<typeof IntentDerivedFrom>;

export const IntentChangeType = z.enum([
  'feature',
  'bugfix',
  'refactor',
  'docs',
  'test',
  'chore',
  'security',
  'perf',
  'mixed',
]);
export type IntentChangeType = z.infer<typeof IntentChangeType>;

export const IntentSourceKind = z.enum(['title', 'body', 'ticket', 'doc', 'commits', 'branch', 'diff']);
export type IntentSourceKind = z.infer<typeof IntentSourceKind>;

export const IntentSourceStatus = z.enum(['used', 'truncated', 'skipped', 'failed']);
export type IntentSourceStatus = z.infer<typeof IntentSourceStatus>;

/** One input the intent layer considered — built by the server, never echoed from the model. */
export const IntentSource = z.object({
  kind: IntentSourceKind,
  /** e.g. the ticket number, the doc path, "title", "branch". */
  ref: z.string(),
  status: IntentSourceStatus,
  /** Human-readable reason (why skipped/failed/truncated). */
  detail: z.string().nullish(),
});
export type IntentSource = z.infer<typeof IntentSource>;

export const Intent = z.object({
  intent: z.string(),
  in_scope: z.array(z.string()),
  out_of_scope: z.array(z.string()),
  /** Dominant kind of change; code-classified by the model. Null on old/legacy rows. */
  change_type: IntentChangeType.nullish(),
  /** Set by code (server/specs/05-intent-layer.md), never by the model. */
  confidence: IntentConfidence.nullish(),
  derived_from: IntentDerivedFrom.nullish(),
  /** Every input considered, in the order they were tried. */
  sources: z.array(IntentSource).nullish(),
});
export type Intent = z.infer<typeof Intent>;

// ---- Blast radius ----
export const ChangedSymbol = z.object({
  name: z.string(),
  file: z.string(),
  kind: z.string(),
});
export type ChangedSymbol = z.infer<typeof ChangedSymbol>;

export const BlastCaller = z.object({
  name: z.string(),
  file: z.string(),
  line: z.number().int(),
});
export type BlastCaller = z.infer<typeof BlastCaller>;

export const DownstreamImpact = z.object({
  symbol: z.string(),
  callers: z.array(BlastCaller),
  endpoints_affected: z.array(z.string()),
  crons_affected: z.array(z.string()),
});
export type DownstreamImpact = z.infer<typeof DownstreamImpact>;

/**
 * Why the index could not give a full answer (mirrors repo-intel's DegradedReason).
 * A degraded blast radius means "unknown", never "no impact".
 */
export const BlastDegradedReason = z.enum([
  'flag_off',
  'index_failed',
  'index_partial',
  'repo_too_large',
  'no_data',
]);
export type BlastDegradedReason = z.infer<typeof BlastDegradedReason>;

export const BlastRadius = z.object({
  changed_symbols: z.array(ChangedSymbol),
  downstream: z.array(DownstreamImpact),
  summary: z.string(),
  /** True when the persistent index could not be used; absent = a full answer. */
  degraded: z.boolean().optional(),
  reason: BlastDegradedReason.optional(),
});
export type BlastRadius = z.infer<typeof BlastRadius>;

// ---- Risks ----
export const RiskSeverity = z.enum(['high', 'medium', 'low']);
export type RiskSeverity = z.infer<typeof RiskSeverity>;

export const Risk = z.object({
  kind: z.string(),
  title: z.string(),
  explanation: z.string(),
  severity: RiskSeverity,
  file_refs: z.array(z.string()),
});
export type Risk = z.infer<typeof Risk>;

export const Risks = z.object({
  risks: z.array(Risk),
});
export type Risks = z.infer<typeof Risks>;

// ---- PR History ----
export const PrHistoryItem = z.object({
  pr_number: z.number().int(),
  title: z.string(),
  merged_at: z.string(),
  author: z.string(),
  files_overlap: z.array(z.string()),
  notes: z.string(),
});
export type PrHistoryItem = z.infer<typeof PrHistoryItem>;

export const PrHistory = z.object({
  history: z.array(PrHistoryItem),
});
export type PrHistory = z.infer<typeof PrHistory>;

// ---- Smart Diff ----
export const SmartDiffRole = z.enum(['core', 'tests', 'wiring', 'docs', 'boilerplate']);
export type SmartDiffRole = z.infer<typeof SmartDiffRole>;

export const SmartDiffFile = z.object({
  path: z.string(),
  pseudocode_summary: z.string().nullish(),
  additions: z.number().int(),
  deletions: z.number().int(),
  finding_lines: z.array(z.number().int()),
});
export type SmartDiffFile = z.infer<typeof SmartDiffFile>;

export const SmartDiffGroup = z.object({
  role: SmartDiffRole,
  files: z.array(SmartDiffFile),
});
export type SmartDiffGroup = z.infer<typeof SmartDiffGroup>;

export const ProposedSplit = z.object({
  name: z.string(),
  files: z.array(z.string()),
});
export type ProposedSplit = z.infer<typeof ProposedSplit>;

export const SmartDiff = z.object({
  groups: z.array(SmartDiffGroup),
  split_suggestion: z.object({
    too_big: z.boolean(),
    total_lines: z.number().int(),
    proposed_splits: z.array(ProposedSplit),
  }),
});
export type SmartDiff = z.infer<typeof SmartDiff>;

// ---- Composed PR Brief (pr_brief.json) ----
/** One "read this first" pointer: a changed file, a 1-based line and why (set by code from the model's output). */
export const ReviewFocusItem = z.object({
  file: z.string(),
  line: z.number().int().min(1),
  reason: z.string().max(160),
});
export type ReviewFocusItem = z.infer<typeof ReviewFocusItem>;

/** An input the brief was generated without, and why. Built by the server, never echoed from the model. */
export const BriefMissingInput = z.object({
  input: z.enum(['intent', 'blast', 'specs', 'linked_issue', 'description']),
  reason: z.enum([
    'not_derived',
    'degraded',
    'no_review_run',
    'none_attached',
    'over_budget',
    'unreadable',
    'not_linked',
    'fetch_failed',
    'empty',
  ]),
  detail: z.string().optional(),
});
export type BriefMissingInput = z.infer<typeof BriefMissingInput>;

export const PrBrief = z.object({
  summary: z.string().max(600),
  /** `Risk.file_refs` entries are `path:start` or `path:start-end`. */
  risks: Risks,
  review_focus: z.array(ReviewFocusItem).max(8),
  /** Snapshots of the data the brief was generated from; null when it was missing. */
  intent: Intent.nullable(),
  blast: BlastRadius.nullable(),
  history: PrHistory.optional(),
  missing_inputs: z.array(BriefMissingInput),
  specs_used: z.array(z.string()),
  head_sha: z.string(),
  generated_at: z.string(),
  prompt_version: z.string(),
  provider: z.string(),
  model: z.string(),
  tokens_in: z.number().int(),
  tokens_out: z.number().int(),
  /** Null when the model is unpriced. */
  cost_usd: z.number().nullable(),
  /** 1, or 2 when the first response failed schema validation and was re-asked. */
  model_requests: z.union([z.literal(1), z.literal(2)]),
});
export type PrBrief = z.infer<typeof PrBrief>;

export const PrBriefResponse = z.object({
  brief: PrBrief.nullable(),
  stale: z.boolean(),
});
export type PrBriefResponse = z.infer<typeof PrBriefResponse>;
