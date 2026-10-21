import type { AgentEvalCase, EvalCaseStatus } from "@devdigest/shared";
import type { IconName } from "@devdigest/ui";

/** Row status: the last run's outcome, or `neverRun` before the first run. */
export type CaseRowStatus = EvalCaseStatus | "neverRun";

export function rowStatus(c: AgentEvalCase): CaseRowStatus {
  return c.last_result?.status ?? "neverRun";
}

export const STATUS_ICON: Record<CaseRowStatus, IconName> = {
  passed: "CheckCircle",
  failed: "XCircle",
  error: "AlertTriangle",
  neverRun: "Clock",
};

export const STATUS_COLOR: Record<CaseRowStatus, string> = {
  passed: "var(--ok)",
  failed: "var(--crit)",
  error: "var(--warn)",
  neverRun: "var(--text-muted)",
};

/** "critical · security" — only the parts the case has; null when it has neither. */
export function severityCategory(c: AgentEvalCase): string | null {
  const parts = [c.expectation.severity, c.expectation.category].filter((p): p is string => !!p);
  return parts.length ? parts.join(" · ") : null;
}
