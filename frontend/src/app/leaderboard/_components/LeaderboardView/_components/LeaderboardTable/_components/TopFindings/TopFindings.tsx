/* TopFindings — the expanded part of a leaderboard row (FR21, D23): up to 3 notable findings of the
   row's window snapshot, worst first — severity · title, `file:line`, the agent and the finding's
   state (accepted ok / dismissed muted / open warn). No findings: "No notable findings recorded."
   Severity and state are text; colour only backs them (NFR4). */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardTopFinding } from "@devdigest/shared";
import { Badge, Icon, SEV } from "@devdigest/ui";
import { STATE_COLOR } from "../../constants";
import { s } from "./styles";

export interface TopFindingsProps {
  findings: readonly LeaderboardTopFinding[];
}

export function TopFindings({ findings }: TopFindingsProps) {
  const t = useTranslations("leaderboardTable");
  if (findings.length === 0) return <p style={s.empty}>{t("detail.empty")}</p>;
  return (
    <ul aria-label={t("detail.heading")} style={s.list}>
      {findings.slice(0, 3).map((f) => {
        const sev = SEV[f.severity];
        const SevIcon = Icon[sev.icon];
        const state = STATE_COLOR[f.state];
        return (
          <li key={f.finding_id} style={s.item}>
            <span aria-hidden style={{ color: sev.c, display: "inline-flex" }}>
              <SevIcon size={14} />
            </span>
            <div style={s.main}>
              <div style={s.title} title={f.title}>
                {t.rich("detail.finding", {
                  severity: t(`severity.${f.severity}`),
                  title: f.title,
                  sev: (chunks) => <span style={{ color: sev.c, fontWeight: 650 }}>{chunks}</span>,
                })}
              </div>
              <div className="mono" style={s.location}>
                {`${f.file}:${f.start_line}`}
              </div>
            </div>
            <span style={s.agent}>{f.agent_name}</span>
            <Badge color={state.c} bg={state.bg}>
              {t(`findingState.${f.state}`)}
            </Badge>
          </li>
        );
      })}
    </ul>
  );
}
