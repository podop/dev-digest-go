import type { ConnTestProvider, GitLabConnection, SecretsStatus } from "@/lib/types";
import {
  LEGACY_CODE_HOST_STEP,
  MODEL_PROVIDERS,
  SETUP_STEPS,
  type CodeHost,
  type ModelProvider,
  type SetupStep,
} from "./constants";

export type StepDone = Record<SetupStep, boolean>;

export const isSetupStep = (v: string | undefined): v is SetupStep => SETUP_STEPS.some((s) => s === v);

/** GitLab connections that hold a token — one of them completes the code-host step (FR38). */
export const gitlabTokenCount = (connections: readonly Pick<GitLabConnection, "token_configured">[]): number =>
  connections.filter((c) => c.token_configured).length;

/** Which steps are complete. `tested` holds providers whose test passed in this view: a passing
 *  test stores the key, but the secrets-status refetch may not have landed yet. A defined custom
 *  provider completes the model step, keyed or not (FR21, D21); a GitHub token or a GitLab connection
 *  with a token completes the code-host step (FR38) — the same rules as the home gate. */
export function stepsDone(
  secrets: SecretsStatus,
  repoCount: number,
  tested: readonly ConnTestProvider[],
  customCount = 0,
  gitlabTokens = 0,
): StepDone {
  return {
    model: customCount > 0 || MODEL_PROVIDERS.some((p) => secrets[p] || tested.includes(p)),
    "code-host": secrets.github || gitlabTokens > 0 || tested.includes("github") || tested.includes("gitlab"),
    repo: repoCount > 0,
  };
}

/** FR22: a missing or invalid `step` opens the first incomplete step (`repo` when all are
 *  complete); a step after the first incomplete one is pulled back to it. The old id `github`
 *  is the code-host step (FR38); the result is always a current id, so the caller can replace the URL with it. */
export function resolveStep(rawRequested: string | undefined, done: StepDone): SetupStep {
  const requested = rawRequested === LEGACY_CODE_HOST_STEP ? "code-host" : rawRequested;
  const firstIncomplete = SETUP_STEPS.find((s) => !done[s]);
  if (!isSetupStep(requested)) return firstIncomplete ?? "repo";
  if (firstIncomplete && SETUP_STEPS.indexOf(requested) > SETUP_STEPS.indexOf(firstIncomplete)) return firstIncomplete;
  return requested;
}

export const previousStep = (step: SetupStep): SetupStep | null => SETUP_STEPS[SETUP_STEPS.indexOf(step) - 1] ?? null;
export const nextStep = (step: SetupStep): SetupStep | null => SETUP_STEPS[SETUP_STEPS.indexOf(step) + 1] ?? null;

/** The provider step 1 opens on: the first one that already has a key, else OpenRouter. */
export function initialProvider(secrets: SecretsStatus): ModelProvider {
  return MODEL_PROVIDERS.find((p) => secrets[p]) ?? "openrouter";
}

/** FR24 warning: a classic token (scopes reported) without the `repo` scope. */
export const lacksRepoScope = (scopes: readonly string[] | null | undefined): boolean =>
  scopes != null && !scopes.includes("repo");

/** The host key of a base URL as the server keys connections (lower-case host, port unless 443), or null when the
 *  input is not an https URL. Used only to find the connection a form submit would update. */
export function hostKeyOf(baseUrl: string): string | null {
  try {
    const url = new URL(baseUrl.trim());
    if (url.protocol !== "https:" || !url.hostname) return null;
    return url.host.toLowerCase().replace(/\.(?=$|:\d+$)/, "");
  } catch {
    return null;
  }
}

/** The connection whose host key equals the base URL's, if any (FR38: "updates the one for that host"). */
export function connectionForUrl(
  connections: readonly GitLabConnection[],
  baseUrl: string,
): GitLabConnection | undefined {
  const key = hostKeyOf(baseUrl);
  return key === null ? undefined : connections.find((c) => c.host === key);
}

/** The host tab the step opens on: GitHub, unless only GitLab is connected. */
export const initialHost = (secrets: SecretsStatus, gitlabTokens: number): CodeHost =>
  !secrets.github && gitlabTokens > 0 ? "gitlab" : "github";

/** "Create a token" link of the GitLab form (FR35): the instance's personal-access-token page with the scopes
 *  DevDigest needs, or null unless the typed base URL is a plain https URL (nothing else becomes a link). */
export function gitlabTokenPageUrl(baseUrl: string): string | null {
  try {
    const url = new URL(baseUrl.trim());
    if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash) return null;
    const root = `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
    return `${root}/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository`;
  } catch {
    return null;
  }
}
