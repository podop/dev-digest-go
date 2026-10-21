import type { IconName } from "@devdigest/ui";
import type { RiskSeverity } from "@devdigest/shared";

/** Severity → icon + colour (high = critical, medium = warning, low = muted). */
export const SEVERITY_META: Record<RiskSeverity, { icon: IconName; color: string }> = {
  high: { icon: "AlertOctagon", color: "var(--crit)" },
  medium: { icon: "AlertTriangle", color: "var(--warn)" },
  low: { icon: "Info", color: "var(--text-muted)" },
};
