/* EvalCasesSection — the "Eval cases" section of an agent's or a skill's Evals tab: header
   (title, "n / m passing", the tab's run control, "New eval case"), the list with its
   loading / error / empty states, the rows, and the editor and delete confirmation as
   siblings of the list (a dialog inside a row would inherit the row's clicks). The tab
   passes its own run control in `runSlot`; it owns the run, this section owns the cases. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Skeleton } from "@devdigest/ui";
import { EVAL_CASES_PER_AGENT_MAX } from "@devdigest/shared/constants/evals";
import { useDeleteEvalCase, useEvalCases, useSkillEvalCases, type EvalCaseOwner } from "@/lib/hooks";
import { DeleteCaseConfirm } from "../DeleteCaseConfirm";
import { EvalCaseEditor } from "../EvalCaseEditor";
import { EvalCaseRow } from "../EvalCaseRow";
import { countPassing } from "./helpers";
import { s } from "./styles";

/** Which dialog is open: the editor on a new case, on an existing one, or neither. */
type Editing = { kind: "new" } | { kind: "edit"; id: string } | null;

export function EvalCasesSection({ owner, runSlot, emptyText }: { owner: EvalCaseOwner; runSlot?: React.ReactNode; emptyText: string }) {
  const t = useTranslations("eval.agentEvals");
  const te = useTranslations("eval.caseEditor");
  const tc = useTranslations("common");
  const agentCases = useEvalCases(owner.kind === "agent" ? owner.id : null);
  const skillCases = useSkillEvalCases(owner.kind === "skill" ? owner.id : null);
  const cases = owner.kind === "agent" ? agentCases : skillCases;
  const remove = useDeleteEvalCase();
  const [editing, setEditing] = React.useState<Editing>(null);
  const [deletingId, setDeletingId] = React.useState<string | null>(null);

  const caseList = cases.data ?? [];
  const edited = editing?.kind === "edit" ? (caseList.find((c) => c.id === editing.id) ?? null) : null;
  const deleting = caseList.find((c) => c.id === deletingId) ?? null;
  const atLimit = caseList.length >= EVAL_CASES_PER_AGENT_MAX;

  const confirmDelete = () => {
    if (deleting) remove.mutate({ id: deleting.id, owner }, { onSuccess: () => setDeletingId(null) });
  };

  return (
    <section style={s.section} aria-label={t("casesHeading")}>
      <div style={s.header}>
        <h2 style={s.title}>
          {t("casesHeading")}
          {!cases.isLoading && !cases.isError && (
            <span style={s.passing}>{t("casesPassing", { passed: countPassing(caseList), total: caseList.length })}</span>
          )}
        </h2>
        {runSlot}
        <span title={atLimit ? t("caseLimit", { max: EVAL_CASES_PER_AGENT_MAX }) : undefined}>
          <Button icon="Plus" disabled={cases.isLoading || cases.isError || atLimit} onClick={() => setEditing({ kind: "new" })}>
            {te("newCase")}
          </Button>
        </span>
      </div>
      {cases.isLoading ? (
        <Skeleton height={140} />
      ) : cases.isError ? (
        <ErrorState title={t("casesLoadError")} onRetry={() => cases.refetch()} retryLabel={tc("actions.retry")} />
      ) : caseList.length === 0 ? (
        <p style={s.caption}>{emptyText}</p>
      ) : (
        <ul style={s.list} aria-label={t("casesHeading")}>
          {caseList.map((c) => (
            <EvalCaseRow key={c.id} evalCase={c} onOpen={() => setEditing({ kind: "edit", id: c.id })} onDelete={() => setDeletingId(c.id)} />
          ))}
        </ul>
      )}

      {editing?.kind === "new" && <EvalCaseEditor owner={owner} onClose={() => setEditing(null)} />}
      {edited && (
        <EvalCaseEditor
          key={edited.id}
          owner={owner}
          evalCase={edited}
          onClose={() => setEditing(null)}
          onDelete={() => {
            setEditing(null);
            setDeletingId(edited.id);
          }}
        />
      )}
      {deleting && (
        <DeleteCaseConfirm name={deleting.name} deleting={remove.isPending} onConfirm={confirmDelete} onCancel={() => setDeletingId(null)} />
      )}
    </section>
  );
}
