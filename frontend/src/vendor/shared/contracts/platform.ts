import { z } from 'zod';
import { CustomProviderId, ModelPricing, Provider } from './knowledge.js';
import { FEATURE_MODEL_IDS } from '../constants/feature-models.js';
import {
  ABOUT_RUNNER_KIND,
  AUTO_REVIEW_MAX_AGENTS,
  SPEND_CAP_MAX_DECIMALS,
  SPEND_CAP_MAX_USD,
  SPEND_CAP_MIN_USD,
} from '../constants/settings.js';
import { RETENTION_DEFAULT_DAYS } from '../constants/workspace.js';

/**
 * Platform / scaffolding DTOs owned by F1:
 *  - settings (GET/PUT /settings, POST /settings/test-connection)
 *  - repos (POST/GET /repos, refresh, delete)
 *  - pulls (GET /repos/:id/pulls, GET /pulls/:id)
 *  - context (Project Context folder)
 */

// ---- Feature → model selection ----
/** System LLM features whose model is selectable in Settings (per-workspace). */
export const FeatureModelId = z.enum(FEATURE_MODEL_IDS);
export type FeatureModelId = z.infer<typeof FeatureModelId>;

/** A chosen provider + model for one feature. */
export const FeatureModelChoice = z.object({
  provider: Provider,
  model: z.string().min(1),
});
export type FeatureModelChoice = z.infer<typeof FeatureModelChoice>;

// Registry (FEATURE_MODELS, FeatureModelDef) lives in the zod-free
// constants/feature-models.ts; re-exported here for compatibility.
export { FEATURE_MODELS, type FeatureModelDef } from '../constants/feature-models.js';
// Spend-cap bounds, error code and settings keys live in the zod-free constants/settings.ts.
export {
  SPEND_CAP_MIN_USD,
  SPEND_CAP_MAX_USD,
  SPEND_CAP_MAX_DECIMALS,
  SPEND_CAP_ERROR_CODE,
  AUTO_REVIEW_MAX_AGENTS,
} from '../constants/settings.js';

// ---- Retention (Settings › Workspace) ----
/** Days a run trace is kept (counted from its run's start); `null` = keep forever. One flat union:
 *  the Go validator answers a bad value with the same single `invalid_union` issue. */
export const RetentionDays = z.union([
  z.literal(30),
  z.literal(60),
  z.literal(90),
  z.literal(180),
  z.literal(365),
  z.null(),
]);
export type RetentionDays = z.infer<typeof RetentionDays>;

// ---- Automatic reviews (Settings › Automatic Reviews) ----
/**
 * The stored `automatic_reviews` value: GET /settings always answers the full object. A stored
 * boolean `b` (the pre-object shape) reads as `{enabled: b, …defaults}`, a missing value as the
 * same with `enabled: false`. `enabled_at` is server-set (null while not armed).
 */
export const AutomaticReviews = z.preprocess(
  (v) => (typeof v === 'boolean' ? { enabled: v } : v),
  z.object({
    enabled: z.boolean().default(false),
    agent_ids: z.array(z.string().uuid()).max(AUTO_REVIEW_MAX_AGENTS).default([]),
    on_new_pr: z.boolean().default(true),
    on_new_commits: z.boolean().default(true),
    enabled_at: z.string().nullable().default(null),
  }),
);
export type AutomaticReviews = z.output<typeof AutomaticReviews>;

/** PUT /settings input for `automatic_reviews`: a boolean (= `{enabled}`) or any subset of the
 *  editable fields; omitted fields keep the stored value, `enabled_at` is never accepted. */
export const AutomaticReviewsInput = z.union([
  z.boolean(),
  z
    .object({
      enabled: z.boolean(),
      agent_ids: z.array(z.string().uuid()).max(AUTO_REVIEW_MAX_AGENTS),
      on_new_pr: z.boolean(),
      on_new_commits: z.boolean(),
    })
    .partial(),
]);
export type AutomaticReviewsInput = z.infer<typeof AutomaticReviewsInput>;

/** GET /settings/automatic-reviews-status — state precedence off > poll_disabled > no_github_token >
 *  no_agents > no_triggers > paused_spend_cap > active; `no_code_host_token` takes the place of
 *  `no_github_token` when the workspace has a GitLab repository (GitLab A FR28). */
export const AutomaticReviewsStatus = z.object({
  state: z.enum([
    'off',
    'poll_disabled',
    'no_github_token',
    'no_code_host_token',
    'no_agents',
    'no_triggers',
    'paused_spend_cap',
    'active',
  ]),
  /** Newest last-polled time of the workspace's repos (ISO UTC ms); null before the first poll. */
  last_poll_at: z.string().nullable(),
  /** Stored open PRs of the workspace, drafts included. */
  prs_tracked: z.number().int().nonnegative(),
  /** Effective polling interval after clamping to 1–1,440 minutes. */
  poll_interval_min: z.number().int().positive(),
});
export type AutomaticReviewsStatus = z.infer<typeof AutomaticReviewsStatus>;

// ---- Settings ----
/**
 * Non-secret prefs/config. Secrets (API keys) are NOT stored here — they go
 * through SecretsProvider (.env in MVP). Settings is a flat key/value bag,
 * surfaced as a typed object for the well-known keys.
 */
export const SettingsKnown = z.object({
  polling_interval_min: z.number().int().min(1).default(5),
  theme: z.enum(['dark', 'light']).default('dark'),
  density: z.enum(['regular', 'compact']).default('regular'),
  sync_to_folder: z.boolean().default(true),
  automatic_reviews: AutomaticReviews.default({}),
  /** Per-feature model overrides (provider+model), keyed by FeatureModelId. */
  feature_models: z.record(FeatureModelId, FeatureModelChoice).default({}),
  /** Workspace default model for features without their own choice; null = built-in defaults. */
  default_model: FeatureModelChoice.nullable().default(null),
  /** Monthly spend cap in USD (0..1,000,000, at most 2 decimals); null = no cap. */
  monthly_spend_cap_usd: z
    .number()
    .min(SPEND_CAP_MIN_USD)
    .max(SPEND_CAP_MAX_USD)
    .refine((v) => Math.abs(v * 10 ** SPEND_CAP_MAX_DECIMALS - Math.round(v * 10 ** SPEND_CAP_MAX_DECIMALS)) < 1e-6, {
      message: `At most ${SPEND_CAP_MAX_DECIMALS} decimals`,
    })
    .nullable()
    .default(null),
  /** Run-trace retention in days (30|60|90|180|365); null = keep forever; absent = 90. */
  retention_days: RetentionDays.default(RETENTION_DEFAULT_DAYS),
});
export type SettingsKnown = z.infer<typeof SettingsKnown>;

/** Full settings payload: well-known keys + arbitrary extras. */
export const Settings = SettingsKnown.passthrough();
export type Settings = z.infer<typeof Settings>;

export const SettingsUpdate = Settings.partial().extend({
  automatic_reviews: AutomaticReviewsInput.optional(),
});
export type SettingsUpdate = z.infer<typeof SettingsUpdate>;

// ---- GitLab connections (specs/2026-10-24-gitlab-a-connection-sync.md §7) ----
/** Limits of a connection (FR1, FR2, FR4). */
export const GITLAB_CONNECTION_LIMITS = {
  maxConnections: 20,
  nameMax: 80,
  baseUrlMax: 2048,
  tokenMin: 1,
  tokenMax: 4096,
} as const;

/** The `ConnTestResult.token_access` verdict (FR11). */
export const GitLabTokenAccess = z.enum(['full', 'read_only']);
export type GitLabTokenAccess = z.infer<typeof GitLabTokenAccess>;

/** Warning codes of a GitLab test, in the order the server lists them (FR11). */
export const GITLAB_TEST_WARNING_CODES = [
  'read_only_token',
  'no_repository_scope',
  'token_expiring',
  'version_unsupported',
  'scopes_unknown',
  'version_unknown',
] as const;
export const GitLabTestWarningCode = z.enum(GITLAB_TEST_WARNING_CODES);
export type GitLabTestWarningCode = z.infer<typeof GitLabTestWarningCode>;

export const GitLabTestWarning = z.object({ code: GitLabTestWarningCode, message: z.string() });
export type GitLabTestWarning = z.infer<typeof GitLabTestWarning>;

/** GET/POST/PUT `/settings/gitlab-connections` response item. The token is write-only: never returned. */
export const GitLabConnection = z.object({
  /** Server-generated UUID. */
  id: z.string(),
  name: z.string(),
  /** Normalised instance URL (FR2): https, no trailing slash, no `/api/v4`. */
  base_url: z.string(),
  /** Host key: lower-cased host name, plus `:<port>` when the port is not 443. */
  host: z.string(),
  token_configured: z.boolean(),
  /** Repositories of all workspaces bound to this host key. */
  repo_count: z.number().int().nonnegative(),
  created_at: z.string(),
  updated_at: z.string(),
});
export type GitLabConnection = z.infer<typeof GitLabConnection>;

const gitLabToken = z
  .string()
  .min(GITLAB_CONNECTION_LIMITS.tokenMin)
  .max(GITLAB_CONNECTION_LIMITS.tokenMax)
  .refine((t) => !/[\s\p{Cc}]/u.test(t), { message: 'No whitespace or control characters' });

/**
 * POST `/settings/gitlab-connections` body. `name` empty or omitted → the host key. `token` is
 * write-only (on PUT: omitted = keep, `null` = remove, a string = replace).
 */
export const GitLabConnectionInput = z.object({
  name: z.string().trim().max(GITLAB_CONNECTION_LIMITS.nameMax).optional(),
  base_url: z.string().min(1).max(GITLAB_CONNECTION_LIMITS.baseUrlMax),
  token: gitLabToken.nullish(),
});
export type GitLabConnectionInput = z.infer<typeof GitLabConnectionInput>;

/** PUT `/settings/gitlab-connections/:id` body: the POST fields, all optional (`base_url` only with the same host key). */
export const GitLabConnectionUpdate = GitLabConnectionInput.partial();
export type GitLabConnectionUpdate = z.infer<typeof GitLabConnectionUpdate>;

/** DELETE `/settings/gitlab-connections/:id` → 200; its repositories become "not connected" (FR7, FR48). */
export const GitLabConnectionDeleted = z.object({
  deleted: z.string(),
  disconnected_repos: z.number().int().nonnegative(),
});
export type GitLabConnectionDeleted = z.infer<typeof GitLabConnectionDeleted>;

// ---- Connection test ----
/** A built-in provider, GitHub, GitLab (a connection, see `connection_id`), or a custom provider id (`custom:<slug>`). */
export const ConnTestProvider = z.union([
  z.enum(['openai', 'anthropic', 'openrouter', 'github', 'gitlab']),
  CustomProviderId,
]);
export type ConnTestProvider = z.infer<typeof ConnTestProvider>;

export const ConnTestRequest = z.object({
  provider: ConnTestProvider,
  /** Optional API key/PAT to persist and then test (BYO key from the UI). */
  key: z.string().min(1).optional(),
  /** Required for `gitlab` (missing → 422 at `["connection_id"]`, unknown → 404 `gitlab_connection_not_found`); ignored otherwise. */
  connection_id: z.string().min(1).optional(),
});
export type ConnTestRequest = z.infer<typeof ConnTestRequest>;

export const ConnTestResult = z.object({
  provider: ConnTestProvider,
  ok: z.boolean(),
  message: z.string(),
  detail: z.unknown().optional(),
  /** Round trip of the provider call; null when ok=false. */
  latency_ms: z.number().int().nullish(),
  /** LLM providers only: the model that was checked and whether the provider lists it (null when ok=false). */
  model: z.string().nullish(),
  model_reachable: z.boolean().nullish(),
  /** GitHub only: the token's login and OAuth scopes (scopes null = not reported, e.g. fine-grained). */
  login: z.string().nullish(),
  scopes: z.array(z.string()).nullish(),
  /** Custom providers: an ok answer that did not verify the key (e.g. "Model list not available; key not verified"). */
  warning: z.string().nullish(),
  /** GitLab only (the spec sends every key, null when not known; GitHub and LLM answers omit them): */
  /** the token's expiry as `YYYY-MM-DD`; null = no expiry or not reported. */
  expires_at: z.string().nullish(),
  /** The instance version (e.g. `17.5.1-ee`); null = not reported. */
  instance_version: z.string().nullish(),
  /** `full` when the scopes contain `api`, `read_only` for `read_api` only, null when the scopes are unknown. */
  token_access: GitLabTokenAccess.nullish(),
  /** In the order of `GITLAB_TEST_WARNING_CODES`; `[]` when none. */
  warnings: z.array(GitLabTestWarning).optional(),
});
export type ConnTestResult = z.infer<typeof ConnTestResult>;

/** The `ConnTestResult` of a GitLab test with every key present (null when `ok` is false). */
export const GitLabConnTestResult = z.object({
  provider: z.literal('gitlab'),
  ok: z.boolean(),
  message: z.string(),
  latency_ms: z.number().int().nullable(),
  login: z.string().nullable(),
  scopes: z.array(z.string()).nullable(),
  expires_at: z.string().nullable(),
  instance_version: z.string().nullable(),
  token_access: GitLabTokenAccess.nullable(),
  warnings: z.array(GitLabTestWarning),
});
export type GitLabConnTestResult = z.infer<typeof GitLabConnTestResult>;

// ---- Monthly spend (GET /settings/spend) ----
/** Month-to-date spend against the optional cap; money rounded to 6 decimals. */
export const SpendStatus = z.object({
  month_start: z.string(),
  spent_usd: z.number(),
  cap_usd: z.number().nullable(),
  remaining_usd: z.number().nullable(),
  cap_reached: z.boolean(),
  /** Answered calls of the month whose cost is unknown (not in `spent_usd`); absent = none. */
  unpriced_count: z.number().int().nonnegative().optional(),
});
export type SpendStatus = z.infer<typeof SpendStatus>;

/** `error.details` of the 402 `spend_cap_reached` answer. */
export const SpendCapDetails = z.object({
  cap_usd: z.number(),
  spent_usd: z.number(),
  month_start: z.string(),
});
export type SpendCapDetails = z.infer<typeof SpendCapDetails>;

// ---- About (GET /about) ----
export const About = z.object({
  version: z.string(),
  build_id: z.string(),
  runner: z.object({ kind: z.literal(ABOUT_RUNNER_KIND), version: z.string() }),
  config_format: z.string(),
  data_dir: z.string(),
  secrets_file: z.string(),
});
export type About = z.infer<typeof About>;

// ---- Secrets status (which provider keys are configured; never the values) ----
/** Boolean per provider: true ⇒ a key/PAT is stored. The value is never exposed. */
export const SecretsStatus = z.object({
  openai: z.boolean(),
  anthropic: z.boolean(),
  openrouter: z.boolean(),
  github: z.boolean(),
});
export type SecretsStatus = z.infer<typeof SecretsStatus>;

// ---- Custom model providers (Settings › API Keys; specs/2026-10-24-custom-model-providers.md §7) ----
export const CustomProviderProtocol = z.enum(['openai', 'anthropic']);
export type CustomProviderProtocol = z.infer<typeof CustomProviderProtocol>;

export const CustomProviderAuthMode = z.enum(['bearer', 'header']);
export type CustomProviderAuthMode = z.infer<typeof CustomProviderAuthMode>;

/** `json_object` is valid only with protocol `openai`. */
export const CustomProviderStructuredOutput = z.enum(['strict', 'json_object', 'prompt_only']);
export type CustomProviderStructuredOutput = z.infer<typeof CustomProviderStructuredOutput>;

/** Limits of the definition (FR1, FR4). */
export const CUSTOM_PROVIDER_LIMITS = {
  maxProviders: 20,
  nameMin: 1,
  nameMax: 80,
  baseUrlMax: 2048,
  apiKeyMin: 1,
  apiKeyMax: 4096,
  maxHeaders: 5,
  headerNameMax: 64,
  headerValueMax: 4096,
  maxModels: 200,
  modelIdMax: 200,
  modelLabelMax: 200,
  priceMax: 10_000,
  priceMaxDecimals: 6,
} as const;

/** A manual model of a custom provider (response). */
export const CustomProviderModel = z.object({
  id: z.string(),
  label: z.string().nullable(),
  pricing: ModelPricing.nullable(),
});
export type CustomProviderModel = z.infer<typeof CustomProviderModel>;

/** An extra header of a custom provider (response): the value is never returned. */
export const CustomProviderHeader = z.object({
  name: z.string(),
  value_configured: z.boolean(),
});
export type CustomProviderHeader = z.infer<typeof CustomProviderHeader>;

/** GET/POST/PUT `/settings/custom-providers` response item. Secret values are never returned. */
export const CustomProvider = z.object({
  /** `custom:<slug>`. */
  id: CustomProviderId,
  name: z.string(),
  protocol: CustomProviderProtocol,
  base_url: z.string(),
  /** True for plain `http` to a host that is not loopback. */
  plain_http: z.boolean(),
  auth_mode: CustomProviderAuthMode,
  /** null for `bearer`. */
  auth_header: z.string().nullable(),
  structured_output: CustomProviderStructuredOutput,
  key_configured: z.boolean(),
  extra_headers: z.array(CustomProviderHeader),
  models: z.array(CustomProviderModel),
  created_at: z.string(),
  updated_at: z.string(),
});
export type CustomProvider = z.infer<typeof CustomProvider>;

const customProviderPrice = z.number().min(0).max(CUSTOM_PROVIDER_LIMITS.priceMax);

/** A manual model in a request: price per 1M tokens in USD, 0–10,000 (decimals are checked by the server). */
export const CustomProviderModelInput = z.object({
  id: z.string().min(1).max(CUSTOM_PROVIDER_LIMITS.modelIdMax),
  label: z.string().max(CUSTOM_PROVIDER_LIMITS.modelLabelMax).nullish(),
  pricing: z.object({ promptPerM: customProviderPrice, completionPerM: customProviderPrice }).nullish(),
});
export type CustomProviderModelInput = z.infer<typeof CustomProviderModelInput>;

/** An extra header in a request: `value` is write-only; omitted = keep the stored value of the same-named header. */
export const CustomProviderHeaderInput = z.object({
  name: z.string().min(1).max(CUSTOM_PROVIDER_LIMITS.headerNameMax),
  value: z.string().min(1).max(CUSTOM_PROVIDER_LIMITS.headerValueMax).optional(),
});
export type CustomProviderHeaderInput = z.infer<typeof CustomProviderHeaderInput>;

/**
 * POST `/settings/custom-providers` body. `api_key` is write-only: on PUT omitted = keep,
 * `null` = remove, a string = replace. Omitted `auth_mode`/`auth_header`/`structured_output`
 * take the server defaults (FR4, FR1).
 */
export const CustomProviderInput = z.object({
  id: CustomProviderId,
  name: z.string().trim().min(CUSTOM_PROVIDER_LIMITS.nameMin).max(CUSTOM_PROVIDER_LIMITS.nameMax),
  protocol: CustomProviderProtocol,
  base_url: z.string().min(1).max(CUSTOM_PROVIDER_LIMITS.baseUrlMax),
  auth_mode: CustomProviderAuthMode.optional(),
  auth_header: z.string().nullish(),
  structured_output: CustomProviderStructuredOutput.optional(),
  api_key: z.string().min(CUSTOM_PROVIDER_LIMITS.apiKeyMin).max(CUSTOM_PROVIDER_LIMITS.apiKeyMax).nullish(),
  extra_headers: z.array(CustomProviderHeaderInput).max(CUSTOM_PROVIDER_LIMITS.maxHeaders).optional(),
  models: z.array(CustomProviderModelInput).max(CUSTOM_PROVIDER_LIMITS.maxModels).optional(),
});
export type CustomProviderInput = z.infer<typeof CustomProviderInput>;

/** PUT `/settings/custom-providers/:id` body: the POST body without `id`. */
export const CustomProviderUpdate = CustomProviderInput.omit({ id: true });
export type CustomProviderUpdate = z.infer<typeof CustomProviderUpdate>;

/** DELETE `/settings/custom-providers/:id` → 200. */
export const CustomProviderDeleted = z.object({ deleted: z.string() });
export type CustomProviderDeleted = z.infer<typeof CustomProviderDeleted>;

/** `error.details` of the 409 `provider_in_use` answer (FR6). */
export const CustomProviderInUseDetails = z.object({
  agents: z.array(z.object({ id: z.string(), name: z.string() })),
  feature_models: z.array(FeatureModelId),
  default_model: z.boolean(),
});
export type CustomProviderInUseDetails = z.infer<typeof CustomProviderInUseDetails>;

// ---- Repos ----
/** The code host of a repository. */
export const RepoProvider = z.enum(['github', 'gitlab']);
export type RepoProvider = z.infer<typeof RepoProvider>;

/** Message of a rejected repository URL (422 `validation_error` at `["url"]`, FR12). */
export const REPO_URL_INVALID_MESSAGE =
  'Must be a GitHub or GitLab repository URL, e.g. https://github.com/owner/repo or https://gitlab.com/group/project';

/** A GitLab repository path has 2 to 21 `/`-separated segments (FR12). */
export const GITLAB_PATH_SEGMENTS = { min: 2, max: 21 } as const;

/** A path segment: `[A-Za-z0-9_.-]+`, not `.`/`..`/`.git`, no leading `-` (the parsed values become clone-path inputs). */
const REPO_SEGMENT_REGEX = /^[A-Za-z0-9_.-]+$/;

/** A path segment that is safe as a directory name: not `.`/`..`/`.git`, no leading `-`. */
function isSafeRepoSegment(s: string): boolean {
  return REPO_SEGMENT_REGEX.test(s) && s !== '.' && s !== '..' && s.toLowerCase() !== '.git' && !s.startsWith('-');
}

/** `https://<host>[:<port>]/<path>` — host labels, optional port. */
const HTTPS_REPO_URL_REGEX = /^https:\/\/([A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?)(?::([0-9]{1,5}))?\/(.+)$/;
/** `git@<host>:<path>` — host name only. */
const SSH_REPO_URL_REGEX = /^git@([A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?):(.+)$/;

/** A parsed repository URL. */
export interface ParsedRepoUrl {
  provider: RepoProvider;
  /** Host key: lower-cased host name, plus `:<port>` for an HTTPS URL with a port other than 443. */
  host: string;
  /** The full path (`owner/name`, or `group/sub/project`) without `.git` and a trailing `/`. */
  path: string;
  /** GitHub: the owner. GitLab: the namespace (every segment but the last). */
  owner: string;
  /** The last path segment. */
  name: string;
}

/**
 * Parse a GitHub or GitLab repository URL (GitLab A FR12): `https://<host>[:<port>]/<path>` or
 * `git@<host>:<path>`, each with an optional `.git` (and, for HTTPS, a trailing `/`). `github.com` takes exactly
 * `owner/repo`; any other host takes 2 to 21 segments. `null` when the text is not such a URL, a segment is unsafe,
 * or it has a query, fragment or userinfo. Host binding to a connection (FR13) is the server's job.
 */
export function parseRepoUrl(url: string): ParsedRepoUrl | null {
  const text = url.trim();
  let host: string;
  let rest: string;
  const https = HTTPS_REPO_URL_REGEX.exec(text);
  if (https) {
    const port = https[2];
    if (port !== undefined && (Number(port) < 1 || Number(port) > 65535)) return null;
    host = https[1]!.toLowerCase() + (port !== undefined && Number(port) !== 443 ? `:${Number(port)}` : '');
    rest = https[3]!;
    if (rest.endsWith('/')) rest = rest.slice(0, -1);
  } else {
    const m = SSH_REPO_URL_REGEX.exec(text);
    if (!m) return null;
    host = m[1]!.toLowerCase();
    rest = m[2]!;
  }
  if (rest.endsWith('.git')) rest = rest.slice(0, -4);
  const segments = rest.split('/');
  if (!segments.every(isSafeRepoSegment)) return null;
  const github = host === 'github.com';
  if (github ? segments.length !== 2 : segments.length < GITLAB_PATH_SEGMENTS.min || segments.length > GITLAB_PATH_SEGMENTS.max) {
    return null;
  }
  const name = segments[segments.length - 1]!;
  return {
    provider: github ? 'github' : 'gitlab',
    host,
    path: segments.join('/'),
    owner: segments.slice(0, -1).join('/'),
    name,
  };
}

export const RepoInput = z.object({
  url: z
    .string()
    .trim()
    .refine((u) => parseRepoUrl(u) !== null, { message: REPO_URL_INVALID_MESSAGE }),
});
export type RepoInput = z.infer<typeof RepoInput>;

/**
 * A tracked repository. `provider`, `host`, `web_url` and `connection_id` are always sent by the server (GitLab A
 * FR16); they are optional here only so pre-GitLab payloads and fixtures stay valid — read them through
 * `@/lib/code-host`, which treats a missing provider as GitHub. For GitLab, `owner` is the namespace full path (may
 * contain `/`) and `owner`, `name`, `full_name` and `web_url` follow the project's current path.
 */
export const Repo = z.object({
  id: z.string(),
  workspace_id: z.string(),
  owner: z.string(),
  name: z.string(),
  full_name: z.string(),
  default_branch: z.string(),
  clone_path: z.string().nullable(),
  last_polled_at: z.string().nullable(),
  created_by: z.string().nullable(),
  provider: RepoProvider.optional(),
  /** Host key, e.g. `github.com`, `gitlab.com`, `git.corp.example:8443`. */
  host: z.string().optional(),
  /** `https://github.com/<full_name>`, or `<base_url>/<full_name>` of a GitLab repository. Links are built from it only. */
  web_url: z.string().optional(),
  /** The connection bound to the repository's host key; null for GitHub and for a not-connected GitLab repository. */
  connection_id: z.string().nullable().optional(),
});
export type Repo = z.infer<typeof Repo>;

// ---- Pull requests ----
export const PrStatus = z.enum(['needs_review', 'reviewed', 'stale', 'open', 'closed', 'merged']);
export type PrStatus = z.infer<typeof PrStatus>;

export const PrMeta = z.object({
  id: z.string().nullish(),
  number: z.number().int(),
  title: z.string(),
  author: z.string(),
  branch: z.string(),
  base: z.string(),
  head_sha: z.string(),
  additions: z.number().int(),
  deletions: z.number().int(),
  files_count: z.number().int(),
  status: PrStatus,
  opened_at: z.string().nullish(),
  updated_at: z.string().nullish(),
  // Latest-review score (list endpoint only; null/absent until reviewed).
  score: z.number().int().nullish(),
  // Total USD cost of ALL runs of the PR (list endpoint only). Unknown run costs
  // are skipped; null when no run has a known cost.
  cost_usd: z.number().nullish(),
  // Latest review (the one `score` comes from) and its date (list endpoint
  // only). Null until the PR has a review.
  latest_review_id: z.string().nullish(),
  last_reviewed_at: z.string().nullish(),
  // The newest review of EACH agent and their findings per severity, summed
  // (list endpoint only). The breakdown counts every finding of those reviews,
  // i.e. what GET /pulls/:id/reviews returns for them. Null until reviewed.
  latest_review_ids: z.array(z.string()).nullish(),
  findings_counts: z
    .object({ CRITICAL: z.number().int(), WARNING: z.number().int(), SUGGESTION: z.number().int() })
    .nullish(),
});
export type PrMeta = z.infer<typeof PrMeta>;

export const PrFile = z.object({
  path: z.string(),
  additions: z.number().int(),
  deletions: z.number().int(),
  patch: z.string().nullish(),
});
export type PrFile = z.infer<typeof PrFile>;

export const PrCommit = z.object({
  sha: z.string(),
  message: z.string(),
  author: z.string(),
  committed_at: z.string().nullish(),
});
export type PrCommit = z.infer<typeof PrCommit>;

export const IssueMeta = z.object({
  number: z.number().int(),
  title: z.string(),
  body: z.string().nullish(),
  state: z.string(),
});
export type IssueMeta = z.infer<typeof IssueMeta>;

export const PrDetail = PrMeta.extend({
  body: z.string().nullish(),
  files: z.array(PrFile),
  commits: z.array(PrCommit),
  linked_issue: IssueMeta.nullish(),
});
export type PrDetail = z.infer<typeof PrDetail>;

// ---- PR review (inline) comments ----
/**
 * A GitHub PR review comment anchored to a diff line. Mirrors the fields the
 * "Files changed" tab needs to render threads inline; `line` is the position in
 * the current diff (null when GitHub can no longer anchor it → `is_outdated`).
 */
export const PrReviewComment = z.object({
  id: z.number().int(),
  path: z.string(),
  line: z.number().int().nullable(),
  original_line: z.number().int().nullable(),
  side: z.enum(['LEFT', 'RIGHT']),
  body: z.string(),
  user: z.string(),
  created_at: z.string(),
  html_url: z.string(),
  in_reply_to_id: z.number().int().nullable(),
  /** GitHub couldn't anchor it to the current diff (line == null). */
  is_outdated: z.boolean(),
});
export type PrReviewComment = z.infer<typeof PrReviewComment>;

/** Body for POST /pulls/:id/comments (create one inline comment / reply). */
export const PrCommentInput = z.object({
  path: z.string().min(1),
  line: z.number().int().positive(),
  side: z.enum(['LEFT', 'RIGHT']).optional(),
  body: z.string().min(1),
  /** Reply to an existing review comment thread (its comment id). */
  in_reply_to: z.number().int().optional(),
});
export type PrCommentInput = z.infer<typeof PrCommentInput>;

/**
 * Repo-intel index state — GET /repos/:id/index-state (JSON of the server's
 * repo-intel `IndexState`). The UI badge + resync completion-poll read it:
 * completion is detected by `lastIndexedSha`/`updatedAt` advancing, since
 * `status` only takes terminal values.
 */
export const RepoIndexState = z.object({
  repoId: z.string(),
  status: z.enum(['full', 'partial', 'degraded', 'failed']),
  filesIndexed: z.number().int(),
  filesSkipped: z.number().int(),
  durationMs: z.number().int(),
  reason: z.string().optional(),
  lastIndexedSha: z.string(),
  indexerVersion: z.number().int(),
  updatedAt: z.string(),
  /** True when the layer is running on the ripgrep fallback. */
  degraded: z.boolean().optional(),
  degradedReason: z
    .enum(['flag_off', 'index_failed', 'index_partial', 'repo_too_large', 'no_data'])
    .optional(),
});
export type RepoIndexState = z.infer<typeof RepoIndexState>;

// ---- Run request (review trigger; owned by A2, contract lives here) ----
export const RunRequest = z.object({
  agentId: z.string().optional(),
  all: z.boolean().optional(),
  /** Explicit agent set (specs/multi-agent-review.md); 2+ ids create a multi-agent
   *  run. Exactly one of agentId / all / agentIds is accepted by the route. */
  agentIds: z.array(z.string()).min(1).max(20).optional(),
});
export type RunRequest = z.infer<typeof RunRequest>;

// ---- Structured API error envelope (returned by the API; UX taxonomy is FE) ----
export const ApiErrorBody = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    details: z.unknown().optional(),
  }),
});
export type ApiErrorBody = z.infer<typeof ApiErrorBody>;
