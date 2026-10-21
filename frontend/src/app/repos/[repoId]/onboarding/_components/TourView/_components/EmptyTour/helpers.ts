import type { FeatureModelChoice, FeatureModelId } from "@devdigest/shared";
// Zod-free subpath: the barrel would pull zod + every schema into this route.
import { FEATURE_MODELS } from "@devdigest/shared/constants/feature-models";

/** Model the Onboarding Tour will use: the Settings choice, else the registry default. */
export function onboardingModel(
  featureModels: Partial<Record<FeatureModelId, FeatureModelChoice>> | null | undefined,
): string | null {
  const chosen = featureModels?.onboarding?.model;
  if (chosen) return chosen;
  return FEATURE_MODELS.find((f) => f.id === "onboarding")?.defaultModel ?? null;
}
