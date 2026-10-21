/* EvalCaseEditor — create (no `evalCase`) or fully edit one eval case, for an agent or a skill
   (FR9–FR11, FR13, FR14). Name, notes, expectation, the pasted diff and PR meta are editable;
   a live preview and inline checks run before Save. The diff, PR meta and finding titles are
   author-steerable text and are only ever rendered as plain text (NFR1). The vendored Modal
   closes on nothing but its X, so the hook adds Escape and the discard prompt. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal, Textarea, TextInput } from "@devdigest/ui";
import { EVAL_CASE_NAME_MAX } from "@devdigest/shared/constants/evals";
import type { AgentEvalCase } from "@devdigest/shared";
import type { EvalCaseOwner } from "@/lib/hooks";
import { DiffPreview } from "./_components/DiffPreview";
import { DiscardConfirm } from "./_components/DiscardConfirm";
import { ExpectationFields } from "./_components/ExpectationFields";
import { InputTabs } from "./_components/InputTabs";
import { formatDate, previewHighlight, previewStatus } from "./helpers";
import { s } from "./styles";
import { useCaseEditor } from "./useCaseEditor";

export function EvalCaseEditor({
  owner,
  evalCase = null,
  onClose,
  onDelete,
}: {
  owner: EvalCaseOwner;
  /** The case to edit; omitted for a new one. */
  evalCase?: AgentEvalCase | null;
  onClose: () => void;
  /** Edit mode: hands the case to the parent's delete confirmation. */
  onDelete?: () => void;
}) {
  const t = useTranslations("eval.caseEditor");
  const tc = useTranslations("common");
  const editor = useCaseEditor({ owner, evalCase, onClose });
  const { values, validity, failure, pending } = editor;

  const lastResult = evalCase?.last_result ?? null;
  const meta = evalCase?.input_meta;

  return (
    <>
      <Modal
        width={860}
        title={evalCase ? t("caseTitle", { name: evalCase.name }) : t("newCase")}
        onClose={editor.requestClose}
        footer={
          <div style={s.footer}>
            {failure?.field === "form" && (
              <span role="alert" style={s.error}>
                {t(`error.${failure.key}`)}
              </span>
            )}
            {evalCase && onDelete && (
              <Button kind="danger" icon="Trash" onClick={onDelete} disabled={pending}>
                {t("delete")}
              </Button>
            )}
            <span style={s.spacer} />
            <Button kind="ghost" onClick={editor.requestClose}>
              {tc("actions.cancel")}
            </Button>
            <Button kind="primary" onClick={editor.save} disabled={!editor.canSave} loading={pending}>
              {pending ? t("saving") : t("save")}
            </Button>
          </div>
        }
      >
        <div style={s.body}>
          <fieldset style={s.fieldset} disabled={pending}>
            {evalCase?.origin === "finding" && (
              <div style={s.facts}>
                <span style={s.tag}>{t("fromFinding")}</span>
                {evalCase.content_edited_at && <span style={s.tag}>{t("edited")}</span>}
                {meta?.repo && <span className="mono">{meta.pr_number == null ? meta.repo : t("repoPr", { repo: meta.repo, number: meta.pr_number })}</span>}
              </div>
            )}

            <label style={s.field}>
              <span style={s.label}>{t("nameLabel")}</span>
              <TextInput id={editor.nameId} value={values.name} onChange={(v) => editor.edit("name", v)} placeholder={t("namePlaceholder")} maxLength={EVAL_CASE_NAME_MAX} />
            </label>
            <label style={s.field}>
              <span style={s.label}>{t("notes")}</span>
              <Textarea value={values.notes} onChange={(v) => editor.edit("notes", v)} rows={3} placeholder={t("notesPlaceholder")} />
            </label>

            <ExpectationFields values={values} validity={validity} analysis={editor.analysis} failure={failure} onEdit={editor.edit} />
            <InputTabs tab={editor.tab} onTab={editor.setTab} values={values} validity={validity} failure={failure} onEdit={editor.edit} />
            {validity.fieldTooLong && (
              <span role="alert" style={s.invalid}>
                {t("check.tooLong")}
              </span>
            )}
            <DiffPreview status={previewStatus(validity, editor.analysis)} analysis={editor.analysis} highlight={previewHighlight(values)} />
          </fieldset>

          {evalCase && (
            <section aria-label={t("lastResultHeading")} style={s.section}>
              <h3 style={s.heading}>{t("lastResultHeading")}</h3>
              <span style={{ fontSize: 13 }}>
                {lastResult ? t("lastResult", { status: t(`status.${lastResult.status}`), date: formatDate(lastResult.ran_at) }) : t("neverRun")}
              </span>
            </section>
          )}
        </div>
      </Modal>
      {editor.confirmingDiscard && <DiscardConfirm onDiscard={onClose} onKeepEditing={editor.keepEditing} />}
    </>
  );
}
