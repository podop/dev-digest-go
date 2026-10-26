import { z } from 'zod';
import { Severity } from './findings.js';
import {
  CiFailOn,
  ConventionCategory,
  ConventionStatus,
  MemoryKind,
  MemoryScope,
  Provider,
  ReviewStrategy,
  SkillType,
} from './knowledge.js';
import {
  PLUGIN_EXPORT_AGENT_IDS_MAX,
  PLUGIN_EXPORT_DESCRIPTION_MAX,
  PLUGIN_EXPORT_NAME_MAX,
  PLUGIN_EXPORT_NAME_MIN,
  PLUGIN_EXPORT_NAME_RE,
  PLUGIN_EXPORT_SKILL_IDS_MAX,
} from '../constants/plugins.js';
import { AgentPeriodMetrics, CostSegment, PeriodInfo, StatPoint } from './observability.js';

/**
 * A6 — Productionize contracts (L08).
 *
 * NEW file (A6 owns it; the barrel re-exports it). Covers:
 *   - PluginBundle / InstalledPlugin   POST /plugins/export, /plugins/import, GET /plugins
 *   - AgentPerf / AgentPerfRow         GET /agents/performance
 *   - Digest / DigestSettings          Weekly Digest (settings + cron-built rows)
 *
 * Nothing here mutates an existing contract — these sit alongside A2's
 * `review-api.ts` and A5's `observability.ts`.
 */

// ---------------------------------------------------------------------------
// Plugin export / import  (.devdigest-plugin/ bundle)
// ---------------------------------------------------------------------------

// Limits, media types and error codes live in the zod-free constants/plugins.ts.
export * from '../constants/plugins.js';

/** One immutable version of an exported skill (workspace export; absent in older bundles). */
export const PluginSkillVersion = z.object({
  version: z.number().int(),
  body: z.string(),
  description: z.string().nullable(),
  message: z.string().nullable(),
  created_at: z.string(),
});
export type PluginSkillVersion = z.infer<typeof PluginSkillVersion>;

/** An exported skill (config only — no DB ids; round-trippable). */
export const PluginSkill = z.object({
  name: z.string(),
  description: z.string(),
  type: z.enum(['rubric', 'convention', 'security', 'custom']),
  source: z.enum(['manual', 'imported_file', 'imported_url', 'extracted', 'community']),
  body: z.string(),
  enabled: z.boolean(),
  evidence_files: z.array(z.string()).nullish(),
  /** Every version, oldest first (workspace export only). */
  versions: z.array(PluginSkillVersion).optional(),
});
export type PluginSkill = z.infer<typeof PluginSkill>;

/** An exported agent. `skills` references PluginSkill.name within the bundle. */
export const PluginAgent = z.object({
  name: z.string(),
  description: z.string(),
  provider: z.enum(['openai', 'anthropic', 'openrouter']),
  model: z.string(),
  system_prompt: z.string(),
  output_schema: z.unknown().nullish(),
  enabled: z.boolean(),
  /** Names of skills (in this bundle) linked to the agent, in order. */
  skills: z.array(z.string()),
  /** Run configuration (workspace export only). */
  strategy: ReviewStrategy.optional(),
  ci_fail_on: CiFailOn.optional(),
  repo_intel: z.boolean().optional(),
});
export type PluginAgent = z.infer<typeof PluginAgent>;

/** An exported eval case. `owner_ref` ties it to an agent or skill by name. */
export const PluginEvalCase = z.object({
  name: z.string(),
  owner_kind: z.enum(['skill', 'agent']),
  owner_ref: z.string(),
  input_diff: z.string().nullish(),
  input_files: z.unknown().nullish(),
  input_meta: z.unknown().nullish(),
  expected_output: z.unknown().nullish(),
  notes: z.string().nullish(),
});
export type PluginEvalCase = z.infer<typeof PluginEvalCase>;

/** An exported convention (house-rule). */
export const PluginConvention = z.object({
  rule: z.string(),
  evidence_path: z.string().nullish(),
  evidence_snippet: z.string().nullish(),
  confidence: z.number().nullish(),
  accepted: z.boolean(),
  /** Workspace export only: the repository (`full_name`), category and review status. */
  repo: z.string().optional(),
  category: ConventionCategory.optional(),
  status: ConventionStatus.optional(),
});
export type PluginConvention = z.infer<typeof PluginConvention>;

/** An exported learning (workspace export): no id, no embedding, no sources. */
export const PluginLearning = z.object({
  scope: MemoryScope,
  kind: MemoryKind,
  content: z.string(),
  confidence: z.number().nullable(),
  /** Repository `full_name`; null for global/team scope. */
  repo: z.string().nullable(),
});
export type PluginLearning = z.infer<typeof PluginLearning>;

/** An exported Project Context file (workspace export). */
export const PluginContextFile = z.object({
  repo: z.string(),
  path: z.string(),
  content: z.string(),
});
export type PluginContextFile = z.infer<typeof PluginContextFile>;

export const PluginManifest = z.object({
  name: z.string(),
  version: z.string(),
  format: z.literal('devdigest-plugin/v1'),
  /** `workspace` for GET /workspace/export; absent in older bundles = `plugin`. */
  kind: z.enum(['plugin', 'workspace']).optional(),
  exported_at: z.string(),
  description: z.string().nullish(),
  counts: z.object({
    agents: z.number().int(),
    skills: z.number().int(),
    eval_cases: z.number().int(),
    conventions: z.number().int(),
    learnings: z.number().int().optional(),
    context_files: z.number().int().optional(),
  }),
});
export type PluginManifest = z.infer<typeof PluginManifest>;

/** The whole `.devdigest-plugin/` bundle as one JSON document. */
export const PluginBundle = z.object({
  manifest: PluginManifest,
  agents: z.array(PluginAgent),
  skills: z.array(PluginSkill),
  eval_cases: z.array(PluginEvalCase),
  conventions: z.array(PluginConvention),
  /** Workspace export only. */
  learnings: z.array(PluginLearning).optional(),
  context_files: z.array(PluginContextFile).optional(),
  /** Allow-listed settings of the workspace export (absent keys omitted). */
  settings: z.record(z.unknown()).optional(),
});
export type PluginBundle = z.infer<typeof PluginBundle>;

/** Request body for POST /plugins/export (Settings slice D, spec §7). Every key optional; no unknown key. */
export const PluginExportRequest = z
  .object({
    /** Kebab-case, 1–64; absent = `devdigest-` + the kebab-cased workspace name. */
    name: z
      .string()
      .min(PLUGIN_EXPORT_NAME_MIN)
      .max(PLUGIN_EXPORT_NAME_MAX)
      .regex(PLUGIN_EXPORT_NAME_RE)
      .optional(),
    description: z.string().max(PLUGIN_EXPORT_DESCRIPTION_MAX).optional(),
    /** Absent (and no skill_ids) = every agent and skill; these agents plus their linked skills otherwise. */
    agent_ids: z.array(z.string().uuid()).min(1).max(PLUGIN_EXPORT_AGENT_IDS_MAX).optional(),
    skill_ids: z.array(z.string().uuid()).min(1).max(PLUGIN_EXPORT_SKILL_IDS_MAX).optional(),
    /** Server default true. */
    include_eval_cases: z.boolean().optional(),
    /** Server default false. */
    include_learnings: z.boolean().optional(),
  })
  .strict();
export type PluginExportRequest = z.infer<typeof PluginExportRequest>;

/** Counts of what a plugin owns / an export holds. */
export const PluginCounts = z.object({
  agents: z.number().int(),
  skills: z.number().int(),
  eval_cases: z.number().int(),
  learnings: z.number().int(),
});
export type PluginCounts = z.infer<typeof PluginCounts>;

/** A row of `installed_plugins` as GET /plugins, PATCH /plugins/:id and an install answer return it. */
export const InstalledPlugin = z.object({
  id: z.string(),
  name: z.string(),
  version: z.string().nullable(),
  /** The uploaded file's name (`filename` query of the import). */
  source: z.string().nullable(),
  installed_at: z.string(),
  enabled: z.boolean(),
  description: z.string().nullable(),
  counts: PluginCounts,
});
export type InstalledPlugin = z.infer<typeof InstalledPlugin>;
/** The name C4 uses for a list row. */
export type PluginRow = InstalledPlugin;

/** The last export, kept as one workspace setting value (`plugin_last_export`). */
export const PluginLastExport = z.object({
  file_name: z.string(),
  exported_at: z.string(),
  counts: PluginCounts,
});
export type PluginLastExport = z.infer<typeof PluginLastExport>;

/** GET /plugins. */
export const PluginList = z.object({
  plugins: z.array(InstalledPlugin),
  last_export: PluginLastExport.nullable(),
});
export type PluginList = z.infer<typeof PluginList>;

/** What a dry run reports: the same counts as the manifest plus `conventions` (installed: 0). */
export const PluginPreviewCounts = z.object({
  agents: z.number().int(),
  skills: z.number().int(),
  eval_cases: z.number().int(),
  conventions: z.number().int(),
  learnings: z.number().int(),
});
export type PluginPreviewCounts = z.infer<typeof PluginPreviewCounts>;

/** POST /plugins/import?dry_run=true → 200; nothing is stored. */
export const PluginImportPreview = z.object({
  manifest: PluginManifest,
  kind: z.enum(['plugin', 'workspace']),
  counts: PluginPreviewCounts,
  agents: z.array(
    z.object({
      name: z.string(),
      provider: Provider,
      model: z.string(),
      enabled: z.boolean(),
      /** Skill names in link order. */
      skills: z.array(z.string()),
    }),
  ),
  skills: z.array(
    z.object({
      name: z.string(),
      type: SkillType,
      description: z.string(),
      enabled: z.boolean(),
    }),
  ),
  conflicts: z.object({
    /** A plugin of this name is already installed. */
    plugin: z.boolean(),
    /** Bundle skill names that equal an existing skill of the workspace. */
    skills: z.array(z.string()),
  }),
  ignored: z.array(z.object({ path_or_section: z.string(), reason: z.string() })),
  warnings: z.array(z.string()),
});
export type PluginImportPreview = z.infer<typeof PluginImportPreview>;

/** `details` of a 409 `plugin_conflict`. */
export const PluginConflictDetails = z.object({
  plugin: z.boolean(),
  skills: z.array(z.string()),
});
export type PluginConflictDetails = z.infer<typeof PluginConflictDetails>;

/** `details` of a 422 `invalid_plugin`: up to 20 broken rules (items assumed to be messages). */
export const PluginInvalidDetails = z.object({
  issues: z.array(z.string()),
});
export type PluginInvalidDetails = z.infer<typeof PluginInvalidDetails>;

/** Result of an install (POST /plugins/import, 201): what was created + the installed_plugins row. */
export const PluginImportResult = z.object({
  installed: InstalledPlugin,
  created: z.object({
    agents: z.number().int(),
    skills: z.number().int(),
    eval_cases: z.number().int(),
    /** Always 0: a bundle's conventions are never installed. */
    conventions: z.number().int(),
    learnings: z.number().int(),
  }),
});
export type PluginImportResult = z.infer<typeof PluginImportResult>;

/** Body of PATCH /plugins/:id. */
export const PluginEnabledRequest = z.object({ enabled: z.boolean() }).strict();
export type PluginEnabledRequest = z.infer<typeof PluginEnabledRequest>;

/** Body of DELETE /plugins/:id: `confirm` must equal the plugin's name (422 `confirmation_mismatch`). */
export const PluginRemoveRequest = z.object({ confirm: z.string() }).strict();
export type PluginRemoveRequest = z.infer<typeof PluginRemoveRequest>;

/** Answer of DELETE /plugins/:id: what the removal deleted. */
export const PluginRemoveResult = z.object({
  ok: z.literal(true),
  removed: PluginCounts.extend({ ci_installations: z.number().int() }),
});
export type PluginRemoveResult = z.infer<typeof PluginRemoveResult>;

// ---------------------------------------------------------------------------
// Agent Performance  (GET /agents/performance)
// ---------------------------------------------------------------------------

/** One dashboard row; `agent_id` null = the "Deleted agents" row. */
export const AgentPerfRow = z.object({
  agent_id: z.string().nullable(),
  agent_name: z.string(),
  metrics: AgentPeriodMetrics,
  /** The same metrics over the equally long window right before the period. */
  previous: AgentPeriodMetrics,
  /** Counted runs per equal-width slice of the period, oldest first; `label` = slice start (ISO). */
  runs_trend: z.array(StatPoint),
});
export type AgentPerfRow = z.infer<typeof AgentPerfRow>;

/** Cross-agent totals of one window (FR6). */
export const PerfSummary = z.object({
  runs: z.number().int(),
  priced_runs: z.number().int(),
  unpriced_runs: z.number().int(),
  accepted: z.number().int(),
  dismissed: z.number().int(),
  pending: z.number().int(),
  decisions: z.number().int(),
  total_cost_usd: z.number().nullable(),
  /** pooled: Σaccepted / Σdecisions; null when Σdecisions = 0. */
  avg_accept_rate: z.number().nullable(),
  most_active: z
    .object({
      agent_id: z.string(),
      agent_name: z.string(),
      runs: z.number().int(),
    })
    .nullable(),
});
export type PerfSummary = z.infer<typeof PerfSummary>;

/** Response of GET /agents/performance. */
export const AgentPerf = z.object({
  period: PeriodInfo,
  min_decisions: z.number().int(),
  cost_basis: z.literal("estimate"),
  summary: PerfSummary,
  /** The same totals over the equally long window right before the period. */
  previous_summary: PerfSummary,
  /** Counted runs of all agents per equal-width slice of the period, oldest first. */
  runs_trend: z.array(StatPoint),
  agents: z.array(AgentPerfRow),
  cost_by_agent: z.array(CostSegment),
  cost_by_model: z.array(CostSegment),
});
export type AgentPerf = z.infer<typeof AgentPerf>;

// re-export Severity-adjacent helper to keep the import surface tidy
export { Severity };

// ---------------------------------------------------------------------------
// Weekly Digest
// ---------------------------------------------------------------------------

/** A persisted digest row (one period summary). */
export const Digest = z.object({
  id: z.string(),
  period_start: z.string().nullable(),
  period_end: z.string().nullable(),
  body_md: z.string().nullable(),
  delivered_to: z.string().nullable(),
});
export type Digest = z.infer<typeof Digest>;

/** Request body for POST /digest/run (build now). */
export const DigestRunRequest = z.object({
  /** ISO range; defaults to the last 7 days. */
  period_start: z.string().optional(),
  period_end: z.string().optional(),
});
export type DigestRunRequest = z.infer<typeof DigestRunRequest>;
