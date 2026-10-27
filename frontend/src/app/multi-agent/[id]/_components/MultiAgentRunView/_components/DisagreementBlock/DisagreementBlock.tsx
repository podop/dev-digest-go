/* DisagreementBlock — "Where agents disagree": one card per location group with a cell per
   agent (severity or "did not flag" + its finding title), a "Show only conflicts" switch and an
   empty state (specs/multi-agent-review.md FR19; Claude Design screen_multiagent.jsx
   ConflictsSection). Titles are model output → text only. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon, SEV, Toggle, type Severity } from "@devdigest/ui";
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
        <Icon.Activity size={14} aria-hidden style={s.headIcon} />
        <h2 style={s.h2}>{t("conflicts.title")}</h2>
        {/* biome-ignore lint/a11y/noLabelWithoutControl: Toggle renders the switch button this label wraps */}
        <label style={s.switchLabel}>
          {t("conflicts.onlyConflicts")}
          <Toggle on={onlyConflicts} onChange={setOnlyConflicts} size={15} />
        </label>
      </div>
      {notComparedCount > 0 && <p style={s.note}>{t("multiAgent.results.notCompared", { count: notComparedCount })}</p>}

      {shown.length === 0 ? (
        <p style={s.note}>{t("conflicts.empty")}</p>
      ) : (
        <ul style={s.list}>
          {shown.map((g) => (
            <li key={`${g.file}:${g.line}:${g.end_line}`} style={s.card}>
              <div style={s.cardHead}>
                <Icon.Code size={13} aria-hidden style={s.codeIcon} />
                <span className="mono" style={s.location}>
                  {groupLocation(g)}
                </span>
                <span style={s.groupTitle}>{g.title}</span>
              </div>
              <ul style={s.takes(g.takes.length)}>
                {g.takes.map((take) => {
                  const flagged = take.verdict !== "ignored";
                  const sev = flagged ? SEV[take.verdict as Severity] : undefined;
                  return (
                    <li key={take.run_id} style={s.take}>
                      <div style={s.persona}>{take.persona || deleted}</div>
                      <div style={s.verdictRow}>
                        <span aria-hidden style={s.dot(sev?.c ?? (flagged ? "var(--warn)" : "var(--text-muted)"))} />
                        <span style={s.verdict(flagged)}>{flagged ? (sev?.label ?? take.verdict) : t("conflicts.didNotFlag")}</span>
                      </div>
                      {take.note && <div style={s.noteText}>{take.note}</div>}
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
