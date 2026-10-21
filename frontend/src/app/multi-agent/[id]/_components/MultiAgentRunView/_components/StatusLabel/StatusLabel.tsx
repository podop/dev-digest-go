/* StatusLabel — a column's status as text (never colour alone); a spinner while it runs. */
import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { AgentColumn } from "@devdigest/shared";
import { STATUS_COLOR } from "./constants";

export function StatusLabel({ status }: { status: AgentColumn["status"] }) {
  const t = useTranslations("runs.multiAgent.results.status");
  const color = STATUS_COLOR[status];
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: 12.5, fontWeight: 600, color }}>
      {status === "running" ? (
        <Icon.RefreshCw size={12} aria-hidden style={{ animation: "ddspin 1s linear infinite" }} />
      ) : (
        <span aria-hidden style={{ width: 7, height: 7, borderRadius: "50%", background: color }} />
      )}
      {t(status)}
    </span>
  );
}
