/* TabFinding — one expandable finding in the Tabs view (Claude Design findings.jsx FindingCard):
   severity, title, category, file:start-end, "NN% conf"; expanded: rationale, suggested fix and
   Accept / Dismiss / Learn / Turn into eval case. Model output is rendered as text or Markdown
   only (NFR6). No "Reply to author" here. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, CategoryTag, ConfidenceNum, Icon, Markdown, SEV, SeverityBadge, type Category, type Severity } from "@devdigest/ui";
import type { FindingActionKind, FindingRecord } from "@devdigest/shared";
import { EvalCaseButton } from "@/components/eval-case-button";
import { s } from "./styles";

export function TabFinding({
  finding: f,
  defaultExpanded = false,
  agentId,
  prId,
  pendingAction,
  onAction,
}: {
  finding: FindingRecord;
  defaultExpanded?: boolean;
  /** The column's agent; null when the agent was deleted. */
  agentId: string | null;
  prId: string;
  /** The action in flight for this finding, if any. */
  pendingAction: FindingActionKind | null;
  onAction: (action: FindingActionKind) => void;
}) {
  const t = useTranslations("prReview");
  const tr = useTranslations("runs.multiAgent.results");
  const [expanded, setExpanded] = React.useState(defaultExpanded);
  const bodyId = React.useId();
  const accepted = !!f.accepted_at;
  const dismissed = !!f.dismissed_at;
  const learned = !!f.memory_id;
  const busy = pendingAction !== null;
  const sev = SEV[f.severity as Severity] ?? SEV.INFO;
  const lines = f.start_line === f.end_line ? `${f.start_line}` : `${f.start_line}-${f.end_line}`;

  return (
    <li data-finding-id={f.id} style={s.card(dismissed ? "var(--border-strong)" : sev.c, accepted || dismissed)}>
      <button type="button" aria-expanded={expanded} aria-controls={bodyId} onClick={() => setExpanded((e) => !e)} style={s.header}>
        <span style={s.badgeWrap}>
          <SeverityBadge severity={f.severity as Severity} compact />
        </span>
        <span style={s.main}>
          <span style={s.titleRow}>
            <span style={s.sevWord(dismissed ? "var(--text-muted)" : sev.c)}>
              {f.severity === "CRITICAL" ? tr("blocker") : sev.label}
            </span>
            <span style={s.title(accepted || dismissed, dismissed)}>{f.title}</span>
            <CategoryTag category={f.category as Category} />
            {accepted && <span style={s.accepted}>{t("finding.accepted")}</span>}
            {dismissed && <span style={s.dismissed}>{t("finding.dismissed")}</span>}
          </span>
          <span style={s.metaRow}>
            <span className="mono" style={s.location}>
              {f.file}:{lines}
            </span>
            <span style={s.conf}>
              <ConfidenceNum value={f.confidence} />
            </span>
          </span>
        </span>
        <Icon.ChevronDown size={16} aria-hidden style={s.chevron(expanded)} />
      </button>

      {expanded && (
        <div id={bodyId} style={s.body}>
          <div style={s.prose}>
            <Markdown>{f.rationale}</Markdown>
          </div>
          {f.suggestion && (
            <div style={s.fix}>
              <Icon.Lightbulb size={13} aria-hidden style={s.fixIcon} />
              <div style={s.fixMain}>
                <div style={s.fixLabel}>{t("finding.suggestedFix")}</div>
                <div style={s.prose}>
                  <Markdown>{f.suggestion}</Markdown>
                </div>
              </div>
            </div>
          )}
          <div style={s.actions}>
            <Button kind="secondary" size="sm" icon="Check" disabled={busy} active={accepted} onClick={() => onAction("accept")}>
              {t("finding.accept")}
            </Button>
            <Button kind="ghost" size="sm" icon="X" disabled={busy} active={dismissed} onClick={() => onAction("dismiss")}>
              {t("finding.dismiss")}
            </Button>
            <Button
              kind="ghost"
              size="sm"
              icon={learned ? "Check" : "Brain"}
              disabled={learned || busy}
              loading={pendingAction === "learn"}
              onClick={() => onAction("learn")}
            >
              {learned ? tr("learned") : t("finding.learn")}
            </Button>
            <EvalCaseButton finding={f} agentId={agentId} prId={prId} />
          </div>
        </div>
      )}
    </li>
  );
}
