import type { IconName } from "@devdigest/ui";
import type { OnboardingSectionId } from "@devdigest/shared/constants/onboarding";

/** Section card icon per section id. */
export const SECTION_ICON: Record<OnboardingSectionId, IconName> = {
  architecture: "Boxes",
  "critical-paths": "Activity",
  "run-locally": "Play",
  "reading-path": "ListChecks",
  "first-tasks": "Target",
};

/** ApiError codes of POST /repos/:id/onboarding → `onboarding.errors.*` message key. */
export const GENERATE_ERROR_KEY = {
  generation_in_progress: "inProgress",
  index_not_ready: "indexNotReady",
  provider_not_configured: "providerNotConfigured",
} as const;

export const GENERATE_ERROR_FALLBACK_KEY = "generationFailed";

/** The only 422 that has a way out: pick a provider key in Settings → Feature models. */
export const PROVIDER_NOT_CONFIGURED = "provider_not_configured";
export const SETTINGS_MODELS_HREF = "/settings/models";

/** The section "in view" is the first one whose top is in the upper 40% of the scroll viewport. */
export const OBSERVER_ROOT_MARGIN = "0px 0px -60% 0px";
