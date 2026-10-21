/* EvalCaseRow — one eval case in an Evals tab list (agent or skill): last-run status
   (icon + text, never colour alone), name, expectation summary, a "manual" tag on an
   agent's hand-written cases, an "edited" marker, severity · category chip, open and
   delete. Case text is rendered as plain text (NFR7). The delete confirmation is the
   parent's sibling modal, not part of the row (dialogs inside a row inherit its clicks). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, IconBtn, Icon } from "@devdigest/ui";
import type { AgentEvalCase } from "@devdigest/shared";
import { rowStatus, severityCategory, STATUS_COLOR, STATUS_ICON } from "./helpers";
import { s } from "./styles";

export function EvalCaseRow({
  evalCase,
  onOpen,
  onDelete,
}: {
  evalCase: AgentEvalCase;
  onOpen: () => void;
  onDelete: () => void;
}) {
  const t = useTranslations("eval.caseRow");
  const status = rowStatus(evalCase);
  const StatusIcon = Icon[STATUS_ICON[status]];
  const { expectation } = evalCase;
  const chip = severityCategory(evalCase);
  return (
    <li style={s.row} data-testid="eval-case-row">
      <span style={{ ...s.status, color: STATUS_COLOR[status] }}>
        <StatusIcon size={15} aria-hidden />
        {t(`status.${status}`)}
      </span>
      <div style={s.main}>
        <button type="button" style={s.name} onClick={onOpen} title={evalCase.name}>
          {evalCase.name}
        </button>
        <span style={s.expectation}>
          {t(`kind.${expectation.kind}`)}{" "}
          <span className="mono">{t("location", { file: expectation.file, start: expectation.start_line, end: expectation.end_line })}</span>
        </span>
      </div>
      {evalCase.owner_kind === "agent" && evalCase.origin === "manual" && <Badge>{t("manual")}</Badge>}
      {evalCase.origin === "finding" && evalCase.content_edited_at && <Badge>{t("edited")}</Badge>}
      {chip && <Badge>{chip}</Badge>}
      <div style={s.actions}>
        <IconBtn icon="Edit" label={t("open", { name: evalCase.name })} onClick={onOpen} />
        <IconBtn icon="Trash" label={t("delete", { name: evalCase.name })} onClick={onDelete} danger />
      </div>
    </li>
  );
}
