import type { AgentColumn } from "@devdigest/shared";

/** Column status → CSS colour token for the status text and its dot/spinner. */
export const STATUS_COLOR: Record<AgentColumn["status"], string> = {
  running: "var(--accent)",
  done: "var(--ok)",
  failed: "var(--crit)",
  cancelled: "var(--text-muted)",
};
