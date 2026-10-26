import type { ConnTestProvider } from "@/lib/types";

export const SETUP_STEPS = ["model", "code-host", "repo"] as const;
export type SetupStep = (typeof SETUP_STEPS)[number];

/** The old id of the code-host step, still accepted in `?step=` (FR38); the view replaces it by `code-host`. */
export const LEGACY_CODE_HOST_STEP = "github";

/** Hosts offered in the code-host step, GitHub first (FR38). */
export const CODE_HOSTS = ["github", "gitlab"] as const;
export type CodeHost = (typeof CODE_HOSTS)[number];

/** Base URL the GitLab form opens with (FR38). */
export const DEFAULT_GITLAB_URL = "https://gitlab.com";

/** Providers offered in step 1, OpenRouter first and preselected (FR23, Q1). */
export const MODEL_PROVIDERS = ["openrouter", "openai", "anthropic"] as const satisfies readonly ConnTestProvider[];
export type ModelProvider = (typeof MODEL_PROVIDERS)[number];

/** Index-state refresh while a new repo has no state (FR25) and the longest wait (EC16). */
export const INDEX_POLL_MS = 3_000;
export const INDEX_POLL_MAX_MS = 5 * 60_000;

export const setupHref = (step: SetupStep) => `/setup?step=${step}`;
export const pullsHref = (repoId: string) => `/repos/${repoId}/pulls`;
