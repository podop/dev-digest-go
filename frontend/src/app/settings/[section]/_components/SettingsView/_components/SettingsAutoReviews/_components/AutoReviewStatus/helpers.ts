import type { AutomaticReviewsStatus } from "@/lib/types";
import type { Tone } from "./styles";

export type PollAge = { unit: "never" | "justNow" } | { unit: "minutes" | "hours" | "days"; count: number };

/** Coarse age of the last poll for "last poll 2m ago" (A13: minute / hour / day steps; keys: autoReviewStatus.lastPoll.*). */
export function pollAge(iso: string | null, now: number = Date.now()): PollAge {
  if (iso === null) return { unit: "never" };
  const minutes = Math.floor(Math.max(0, now - new Date(iso).getTime()) / 60_000);
  if (minutes < 1) return { unit: "justNow" };
  if (minutes < 60) return { unit: "minutes", count: minutes };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { unit: "hours", count: hours };
  return { unit: "days", count: Math.floor(hours / 24) };
}

type State = AutomaticReviewsStatus["state"];

/** Tone of the dot: green when running, muted when the user turned it off, warn when something blocks it. */
export function toneOf(state: State): Tone {
  if (state === "active") return "ok";
  return state === "off" ? "muted" : "warn";
}

/** The Settings section a paused state sends the user to, if any (§9). */
export function fixSectionOf(state: State): "github" | "api-keys" | null {
  if (state === "no_github_token") return "github";
  return state === "paused_spend_cap" ? "api-keys" : null;
}
