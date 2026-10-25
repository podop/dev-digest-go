import type { CiExportOptions } from "@/lib/hooks";

export const MODAL_WIDTH = 860;

/** The wizard's four steps, in order; their labels are `ci.exportWizard.steps.<key>`. */
export const STEP_KEYS = ["target", "preview", "configure", "install"] as const;
export const LAST_STEP = STEP_KEYS.length - 1;

/** `pull_request` activity types the workflow can run on (FR11). */
export const TRIGGERS = ["opened", "synchronize", "reopened"] as const;
export type Trigger = (typeof TRIGGERS)[number];
export type TriggerState = Record<Trigger, boolean>;

/** Defaults: `opened` + `synchronize` on, `reopened` off (FR11). */
export const DEFAULT_TRIGGERS: TriggerState = { opened: true, synchronize: true, reopened: false };

export type PostAs = CiExportOptions["post_as"];
/** Order of the "Post results as" options; the first is the default and "recommended". */
export const POST_AS_OPTIONS: readonly { value: PostAs; labelKey: "githubReview" | "prComment" | "none" }[] = [
  { value: "github_review", labelKey: "githubReview" },
  { value: "pr_comment", labelKey: "prComment" },
  { value: "none", labelKey: "none" },
];
export const DEFAULT_POST_AS: PostAs = "github_review";

export type InstallMethod = "pr" | "zip";

/** Secrets the workflow reads; `GITHUB_TOKEN` is provided by Actions (FR11). */
export const SECRET_KEYS = ["OPENROUTER_API_KEY", "GITHUB_TOKEN"] as const;
export const OPENROUTER_SECRET = SECRET_KEYS[0];

/** Paths the export always contains (FR6). */
export const RUNNER_BUNDLE_PATH = ".devdigest/runner/index.js";
export const WORKFLOW_PATH_PATTERN = /^\.github\/workflows\/devdigest-review-(.+)\.yml$/;
