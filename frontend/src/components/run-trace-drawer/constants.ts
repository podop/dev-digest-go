/** Constants for the Run Trace + Live Log drawer (A5). */
import type { ProjectContextDocStatus } from "@devdigest/shared";

/** Drawer width (px). */
export const DRAWER_WIDTH = 720;

/** Live-log stream viewport height (px). */
export const LOG_HEIGHT = 420;

/** Tab keys (Trace / Live log). */
export const TABS = ["trace", "log"] as const;
export type TraceTab = (typeof TABS)[number];

/** Prompt-assembly block accent colours (by leg). */
export const PROMPT_COLORS = {
  system: "var(--text-muted)",
  intent: "var(--accent)",
  skills: "var(--accent)",
  memory: "var(--warn)",
  repoMap: "var(--accent)",
  specs: "var(--text-secondary)",
  callers: "var(--warn)",
  user: "var(--ok)",
} as const;

/** Project-context document status → badge colours (`included` is the only one whose text was sent). */
export const CONTEXT_STATUS_COLORS = {
  included: { color: "var(--ok)", bg: "var(--ok-bg)" },
  missing: { color: "var(--warn)", bg: "var(--warn-bg)" },
  too_large: { color: "var(--warn)", bg: "var(--warn-bg)" },
  over_budget: { color: "var(--warn)", bg: "var(--warn-bg)" },
  unreadable: { color: "var(--crit)", bg: "var(--crit-bg)" },
} as const satisfies Record<ProjectContextDocStatus, { color: string; bg: string }>;
