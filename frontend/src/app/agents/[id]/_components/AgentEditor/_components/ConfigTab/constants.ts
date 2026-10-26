import type { CiFailOn, ReviewStrategy } from "@devdigest/shared";

/** Selectable review strategies (labels are i18n'd in the component). */
export const STRATEGY_VALUES: readonly ReviewStrategy[] = ["single-pass", "map-reduce", "auto"];

/** CI gate policy options — when a CI review blocks/fails (labels i18n'd). */
export const CI_FAIL_ON_VALUES: readonly CiFailOn[] = ["never", "critical", "warning", "any"];
