/* TabFinding — one expandable finding in the Tabs view: category, file:start-end, "NN% conf",
   rationale, suggested fix and Accept / Dismiss / Learn / Turn into eval case. Model output is
   rendered as text or Markdown only (NFR6). No "Reply to author" here. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, CategoryTag, ConfidenceNum, Icon, Markdown, SeverityBadge, type Category, type Severity } from "@devdigest/ui";
import type { FindingActionKind, FindingRecord } from "@devdigest/shared";
import { EvalCaseButton } from "@/components/eval-case-button";
import { s } from "./styles";

export function TabFinding({
  finding: f,
  agentId,
  prId,
  pendingAction,
  onAction,
}: {
  finding: FindingRecord;
  /** The column's agent; null when the agent was deleted. */
  agentId: string | null;
  prId: string;
  /** The action in flight for this finding, if any. */
  pendingAction: FindingActionKind | null;
  onAction: (action: FindingActionKind) => void;
}) {
  const t = useTranslations("prReview");
  const tr = useTranslations("runs.multiAgent.results");
  const [expanded, setExpanded] = React.useState(false);
  const bodyId = React.useId();
  const accepted = !!f.accepted_at;
  const dismissed = !!f.dismissed_at;
  const learned = !!f.memory_id;
  const busy = pendingAction !== null;
  const lines = f.start_line === f.end_line ? `${f.start_line}` : `${f.start_line}-${f.end_line}`;

  return (
    <li data-finding-id={f.id} style={s.card(accepted || dismissed)}>
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={bodyId}
        onClick={() => setExpanded((e) => !e)}
        style={s.header}
      >
        <SeverityBadge severity={f.severity as Severity} />
        <span style={s.title}>{f.title}</span>
        {accepted && <span style={s.tag}>{t("finding.accepted")}</span>}
        {dismissed && <span style={s.tag}>{t("finding.dismissed")}</span>}
        <Icon.ChevronDown
          size={16}
          aria-hidden
          style={{ transform: expanded ? "rotate(180deg)" : "none", transition: "transform .15s" }}
        />
      </button>

      {expanded && (
        <div id={bodyId} style={s.body}>
          <div style={s.metaRow}>
            <CategoryTag category={f.category as Category} />
            <span className="mono">
              {f.file}:{lines}
            </span>
            <ConfidenceNum value={f.confidence} />
          </div>
          <div style={s.prose}>
            <Markdown>{f.rationale}</Markdown>
          </div>
          {f.suggestion && (
            <div style={s.prose}>
              <div style={s.fixLabel}>{t("finding.suggestedFix")}</div>
              <Markdown>{f.suggestion}</Markdown>
            </div>
          )}
          <div style={s.actions}>
            <Button
              kind="secondary"
              size="sm"
              icon="Check"
              disabled={busy}
              active={accepted}
              onClick={() => onAction("accept")}
            >
              {t("finding.accept")}
            </Button>
            <Button kind="ghost" size="sm" icon="X" disabled={busy} active={dismissed} onClick={() => onAction("dismiss")}>
              {t("finding.dismiss")}
            </Button>
            <Button
              kind="ghost"
              size="sm"
              icon={learned ? "Check" : "Lightbulb"}
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
