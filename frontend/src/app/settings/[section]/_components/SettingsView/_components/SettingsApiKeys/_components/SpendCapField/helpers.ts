import {
  SPEND_CAP_MAX_DECIMALS,
  SPEND_CAP_MAX_USD,
  SPEND_CAP_MIN_USD,
} from "@devdigest/shared/constants/settings";

/** Why a typed cap was rejected; maps to `spendCap.errors.<error>`. */
export type CapError = "notNumber" | "decimals" | "range";

/** Result of reading the cap input: empty = no cap, a valid amount, or the reason it is invalid. */
export type CapParse =
  | { kind: "clear" }
  | { kind: "value"; value: number }
  | { kind: "error"; error: CapError };

/**
 * Read the cap input (EC18): "$" and spaces are stripped, empty means no cap, anything
 * non-numeric, negative, above the maximum or with more decimals than allowed is an error.
 */
export function parseCap(raw: string): CapParse {
  const cleaned = raw.replace(/[$\s]/g, "");
  if (cleaned === "") return { kind: "clear" };
  const m = /^(-?)(\d*)(?:\.(\d*))?$/.exec(cleaned);
  const frac = m?.[3] ?? "";
  if (!m || (m[2] === "" && frac === "")) return { kind: "error", error: "notNumber" };
  if (frac.length > SPEND_CAP_MAX_DECIMALS) return { kind: "error", error: "decimals" };
  const value = Number(cleaned);
  if (m[1] === "-" || value < SPEND_CAP_MIN_USD || value > SPEND_CAP_MAX_USD) {
    return { kind: "error", error: "range" };
  }
  return { kind: "value", value };
}

/** Money for display (A13): two decimals, half up, like the backend's cap message. */
export function formatMoney(usd: number): string {
  // toFixed(4) first: it absorbs binary noise (1.005 * 100 = 100.49999…) before rounding to cents.
  const cents = Math.round(Number((usd * 100).toFixed(4)));
  return `$${(cents / 100).toFixed(2)}`;
}

/** Whole-dollar bound for the range message, e.g. 1000000 → "$1,000,000". */
export function formatBound(usd: number): string {
  return `$${usd.toLocaleString("en-US")}`;
}

/** "2026-10-01T00:00:00.000Z" → "2026-10-01". */
export function monthStartDay(monthStart: string): string {
  return monthStart.slice(0, 10);
}
