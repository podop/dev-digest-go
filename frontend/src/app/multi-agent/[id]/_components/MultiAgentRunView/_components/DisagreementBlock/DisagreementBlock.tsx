/* DisagreementBlock — "Where agents disagree": one card per location group with a cell per
   agent (severity or "did not flag" + its finding title), a "Show only conflicts" toggle and an
   empty state (specs/multi-agent-review.md FR19). Titles are model output → text only. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Checkbox, SeverityBadge, type Severity } from "@devdigest/ui";
import type { Conflict } from "@devdigest/shared";
import { groupLocation, visibleGroups } from "./helpers";
import { s } from "./styles";

export function DisagreementBlock({
  groups,
  notComparedCount,
}: {
  groups: readonly Conflict[];
  /** Agents that failed, were cancelled or are still running — they are not compared. */
  notComparedCount: number;
}) {
  const t = useTranslations("runs");
  const [onlyConflicts, setOnlyConflicts] = React.useState(false);
  const shown = visibleGroups(groups, onlyConflicts);
  const deleted = t("multiAgent.results.deletedAgent");

  return (
    <section style={s.block} aria-label={t("conflicts.title")}>
      <div style={s.head}>
        <h2 style={s.h2}>{t("conflicts.title")}</h2>
        <Checkbox checked={onlyConflicts} onChange={setOnlyConflicts} label={t("conflicts.onlyConflicts")} />
      </div>
      {notComparedCount > 0 && <p style={s.note}>{t("multiAgent.results.notCompared", { count: notComparedCount })}</p>}

      {shown.length === 0 ? (
        <p style={s.note}>{t("conflicts.empty")}</p>
      ) : (
        <ul style={s.list}>
          {shown.map((g) => (
            <li key={`${g.file}:${g.line}:${g.end_line}`} style={s.card}>
              <div style={s.cardHead}>
                <span className="mono" style={s.location}>
                  {groupLocation(g)}
                </span>
                <span style={s.groupTitle}>{g.title}</span>
              </div>
              <ul style={s.takes}>
                {g.takes.map((take) => (
                  <li key={take.run_id} style={s.take}>
                    <span style={s.persona}>{take.persona || deleted}</span>
                    {take.verdict === "ignored" ? (
                      <span style={s.ignored}>{t("conflicts.didNotFlag")}</span>
                    ) : (
                      <>
                        <SeverityBadge severity={take.verdict as Severity} />
                        <span style={s.noteTitle}>{take.note}</span>
                      </>
                    )}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
