import type { ConnTestProvider, SecretsStatus } from "@/lib/types";
import { MODEL_PROVIDERS, SETUP_STEPS, type ModelProvider, type SetupStep } from "./constants";

export type StepDone = Record<SetupStep, boolean>;

export const isSetupStep = (v: string | undefined): v is SetupStep => SETUP_STEPS.some((s) => s === v);

/** Which steps are complete. `tested` holds providers whose test passed in this view: a passing
 *  test stores the key, but the secrets-status refetch may not have landed yet. A defined custom
 *  provider completes the model step, keyed or not (FR21, D21) — the same rule as the home gate. */
export function stepsDone(
  secrets: SecretsStatus,
  repoCount: number,
  tested: readonly ConnTestProvider[],
  customCount = 0,
): StepDone {
  return {
    model: customCount > 0 || MODEL_PROVIDERS.some((p) => secrets[p] || tested.includes(p)),
    github: secrets.github || tested.includes("github"),
    repo: repoCount > 0,
  };
}

/** FR22: a missing or invalid `step` opens the first incomplete step (`repo` when all are
 *  complete); a step after the first incomplete one is pulled back to it. */
export function resolveStep(requested: string | undefined, done: StepDone): SetupStep {
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
