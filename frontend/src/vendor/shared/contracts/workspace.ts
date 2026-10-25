import { z } from 'zod';
import { workspaceNameProblem } from '../constants/workspace.js';

/**
 * Settings › Workspace (specs/2026-10-23-settings-b-workspace.md §7):
 *   GET /workspace · PUT /workspace · GET /workspace/stats · POST /workspace/reset-history.
 * GET /workspace/export answers a file whose body is a `PluginBundle` (contracts/productionize).
 * The camelCase keys of GET /workspace are the pre-existing wire casing; the rest is snake_case.
 */

// Runtime values live in the zod-free constants/workspace.ts; re-exported for convenience.
export {
  WORKSPACE_NAME_MIN,
  WORKSPACE_NAME_MAX,
  WORKSPACE_SWITCHER_NAME_MAX,
  RETENTION_DAYS_OPTIONS,
  RETENTION_DEFAULT_DAYS,
  WORKSPACE_RESET_MISMATCH_CODE,
  WORKSPACE_RESET_ACTIVE_CODE,
  WORKSPACE_BUNDLE_KIND,
  normalizeWorkspaceName,
  workspaceNameProblem,
  type WorkspaceNameProblem,
} from '../constants/workspace.js';

/** A display name: trimmed, 1–80 code points, no control characters. */
export const WorkspaceName = z
  .string()
  .trim()
  .superRefine((value, ctx) => {
    const problem = workspaceNameProblem(value);
    if (problem) ctx.addIssue({ code: z.ZodIssueCode.custom, message: problem });
  });
export type WorkspaceName = z.infer<typeof WorkspaceName>;

/** One repository row of GET /workspace. */
export const WorkspaceRepo = z.object({
  id: z.string(),
  full_name: z.string(),
  clone_path: z.string().nullable(),
  last_polled_at: z.string().nullable(),
  cloned: z.boolean(),
});
export type WorkspaceRepo = z.infer<typeof WorkspaceRepo>;

/** GET /workspace and the answer of PUT /workspace. */
export const WorkspaceInfo = z.object({
  workspaceId: z.string(),
  /** The display name (not the lookup key `default`). */
  name: z.string(),
  /** Absolute data directory (read-only in the UI). */
  dataDir: z.string(),
  cloneDir: z.string(),
  repos: z.array(WorkspaceRepo),
});
export type WorkspaceInfo = z.infer<typeof WorkspaceInfo>;

/** Body of PUT /workspace (no other key allowed). */
export const WorkspaceRenameRequest = z.object({ name: WorkspaceName }).strict();
export type WorkspaceRenameRequest = z.infer<typeof WorkspaceRenameRequest>;

const Count = z.number().int().nonnegative();

/** GET /workspace/stats. */
export const WorkspaceStats = z.object({
  agents: Count,
  skills: Count,
  eval_cases: Count,
  learnings: Count,
  repos: Count,
  runs: Count,
});
export type WorkspaceStats = z.infer<typeof WorkspaceStats>;

/** Body of POST /workspace/reset-history: the workspace name typed exactly (no trim, case-sensitive). */
export const WorkspaceResetRequest = z.object({ confirm: z.string() }).strict();
export type WorkspaceResetRequest = z.infer<typeof WorkspaceResetRequest>;

/** Rows removed by a reset, per kind. */
export const WorkspaceResetDeleted = z.object({
  agent_runs: Count,
  run_traces: Count,
  reviews: Count,
  findings: Count,
  multi_agent_runs: Count,
  eval_suite_runs: Count,
  eval_runs: Count,
  ci_runs: Count,
  composed_reviews: Count,
  learnings: Count,
});
export type WorkspaceResetDeleted = z.infer<typeof WorkspaceResetDeleted>;

/** 200 answer of POST /workspace/reset-history. */
export const WorkspaceResetResult = z.object({
  deleted: WorkspaceResetDeleted,
  prs_unmarked: Count,
});
export type WorkspaceResetResult = z.infer<typeof WorkspaceResetResult>;
