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

/** Provided by Actions, never set by the user (FR11); the other secrets and the variables come from the export response (FR31). */
export const GITHUB_TOKEN_SECRET = "GITHUB_TOKEN";

export type Runner = NonNullable<CiExportOptions["runner"]>;
/** Order of the "Runner" options; their labels are `ci.exportWizard.runner.<labelKey>`. */
export const RUNNER_OPTIONS: readonly { value: Runner; labelKey: "githubHosted" | "selfHosted" }[] = [
  { value: "github_hosted", labelKey: "githubHosted" },
  { value: "self_hosted", labelKey: "selfHosted" },
];

/** Paths the export always contains (FR6). */
export const RUNNER_BUNDLE_PATH = ".devdigest/runner/index.js";
export const WORKFLOW_PATH_PATTERN = /^\.github\/workflows\/devdigest-review-(.+)\.yml$/;

/** GitLab CI export (GitLab C FR6, FR41): the editable job file and the read-only runner bundle (large base64, never rendered). */
export const GITLAB_JOB_PATH_PATTERN = /^\.gitlab\/devdigest\/review-(.+)\.yml$/;
export const GITLAB_RUNNER_BUNDLE_PATH = ".devdigest/runner/devdigest-runner-linux-amd64.gz.b64";
/** The posting token the GitLab job reads; listed by the server in `required_secrets` unless "Don't post" (FR43). */
export const GITLAB_TOKEN_VARIABLE = "DEVDIGEST_GITLAB_TOKEN";
