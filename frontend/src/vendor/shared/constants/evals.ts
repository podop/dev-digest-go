/**
 * Zod-free eval limits, period presets and the trend cap, re-exported by contracts/eval-pipeline.
 * Import this subpath (`@devdigest/shared/constants/evals`) from client bundles that must
 * not pull zod in.
 */
export const EVAL_PERIODS = ['7d', '30d', '90d', 'all'] as const;
export type EvalPeriodId = (typeof EVAL_PERIODS)[number];
/** Most runs the agent dashboard `trend` carries when a `period` is given. */
export const EVAL_PERIOD_TREND_MAX = 100;

// ---- case limits (shared by the server contract and the client editor) ----
export const EVAL_CASE_NAME_MAX = 120;
/** Semantic limit on the STORED diff (checked by the service as `patch_too_large`, not by the schema). */
export const EVAL_INPUT_DIFF_MAX_CHARS = 200_000;
/** Cases one owner (agent or skill) can have. */
export const EVAL_CASES_PER_AGENT_MAX = 200;
export const EVAL_EXPECTATION_FILE_MAX = 1000;
export const EVAL_EXPECTATION_TITLE_MAX = 300;
export const EVAL_PR_TITLE_MAX = 300;
export const EVAL_PR_DESCRIPTION_MAX = 20_000;
/** Most diff paths `expectation_file_not_in_diff` carries in `details.files`. */
export const EVAL_DIFF_FILES_DETAIL_MAX = 50;
