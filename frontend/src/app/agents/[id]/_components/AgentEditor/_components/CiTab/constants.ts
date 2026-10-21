import type { CiFailOn } from "@devdigest/shared";

/** CI gate policy options of the "Fail CI on" select (labels come from agents.config.ciFailOnOptions). */
export const CI_FAIL_ON_VALUES: readonly CiFailOn[] = ["never", "critical", "warning", "any"];
