import type { Settings } from "@/lib/types";
import { DEFAULT_INTERVAL_MIN } from "./constants";

/** The stored polling interval in minutes, or the server default while it is not stored. */
export function intervalOf(settings: Pick<Settings, "polling_interval_min">): number {
  return settings.polling_interval_min ?? DEFAULT_INTERVAL_MIN;
}

/** The two trigger flags of the stored `automatic_reviews` (what TriggerConditions shows). */
export function triggersOf(automatic: Pick<Settings["automatic_reviews"], "on_new_pr" | "on_new_commits">) {
  return { on_new_pr: automatic.on_new_pr, on_new_commits: automatic.on_new_commits };
}
