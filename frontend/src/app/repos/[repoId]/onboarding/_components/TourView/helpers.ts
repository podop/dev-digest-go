import type { OnboardingTourReady, OnboardingTourState } from "@devdigest/shared";
import { ApiError } from "@/lib/api";
import { GENERATE_ERROR_FALLBACK_KEY, GENERATE_ERROR_KEY } from "./constants";

const RELATIVE_UNITS: readonly (readonly [Intl.RelativeTimeFormatUnit, number])[] = [
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
];
const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** "2 hours ago" / "now" for an ISO timestamp; "—" when it does not parse. English only (spec Q1). */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "—";
  const seconds = Math.max(0, Math.floor((now - then) / 1000));
  for (const [unit, size] of RELATIVE_UNITS) {
    if (seconds >= size) return rtf.format(-Math.floor(seconds / size), unit);
  }
  return rtf.format(0, "second");
}

/** The tour page URL, with `#<section>` when a section is in view. */
export function shareUrl(origin: string, pathname: string, section: string | null): string {
  return `${origin}${pathname}${section ? `#${section}` : ""}`;
}

/** First id (in display order) that is currently visible, else null. */
export function firstVisible<T extends string>(ids: readonly T[], visible: ReadonlySet<string>): T | null {
  return ids.find((id) => visible.has(id)) ?? null;
}

/** `#section` of a location hash when it names one of `ids`. */
export function sectionFromHash<T extends string>(ids: readonly T[], hash: string): T | null {
  const id = hash.replace(/^#/, "");
  return ids.find((x) => x === id) ?? null;
}

export interface GenerateErrorInfo {
  /** Key under `onboarding.errors.*`. */
  messageKey: (typeof GENERATE_ERROR_KEY)[keyof typeof GENERATE_ERROR_KEY] | typeof GENERATE_ERROR_FALLBACK_KEY;
  /** ApiError code, shown next to the message; undefined for a non-API failure. */
  code: string | undefined;
}

/** Maps a failed generate call (409 / 422 / 502 / network) to its message key and code. */
export function describeGenerateError(err: unknown): GenerateErrorInfo {
  const code = err instanceof ApiError ? err.code : undefined;
  const known = code && code in GENERATE_ERROR_KEY ? GENERATE_ERROR_KEY[code as keyof typeof GENERATE_ERROR_KEY] : undefined;
  return { messageKey: known ?? GENERATE_ERROR_FALLBACK_KEY, code };
}

/** The stored tour of a GET/POST answer, or null for `{ status: 'none' }` / no data yet. */
export function readyTour(state: OnboardingTourState | undefined): OnboardingTourReady | null {
  return state?.status === "ready" ? state : null;
}
