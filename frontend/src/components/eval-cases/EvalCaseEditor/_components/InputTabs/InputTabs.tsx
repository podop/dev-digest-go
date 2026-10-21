/* InputTabs — the case input: the pasted Diff (with its character counter and checks) and
   the PR meta the engine sees (title, description). Both are plain text (NFR1). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Tabs, Textarea, TextInput } from "@devdigest/ui";
import { EVAL_INPUT_DIFF_MAX_CHARS, EVAL_PR_TITLE_MAX } from "@devdigest/shared/constants/evals";
import { formatCount, type CaseValidity, type EditorValues, type SaveError } from "../../helpers";
import type { InputTab } from "../../useCaseEditor";
import { s } from "../../styles";

export function InputTabs({
  tab,
  onTab,
  values,
  validity,
  failure,
  onEdit,
}: {
  tab: InputTab;
  onTab: (tab: InputTab) => void;
  values: EditorValues;
  validity: CaseValidity;
  failure: SaveError | null;
  onEdit: <K extends keyof EditorValues>(field: K, value: EditorValues[K]) => void;
}) {
  const t = useTranslations("eval.caseEditor");
  const over = values.diff.length > EVAL_INPUT_DIFF_MAX_CHARS;
  const diffError = validity.noHunk ? t("check.noHunk") : failure?.field === "diff" ? t(`error.${failure.key}`, { max: formatCount(EVAL_INPUT_DIFF_MAX_CHARS) }) : null;

  return (
    <section aria-label={t("inputLabel")} style={s.section}>
      <h3 style={s.heading}>{t("inputLabel")}</h3>
      <Tabs
        pad="0"
        tabs={[
          { key: "diff", label: t("tabs.diff") },
          { key: "meta", label: t("tabs.prMeta") },
        ]}
        value={tab}
        onChange={(k) => onTab(k as InputTab)}
      />
      {tab === "diff" ? (
        <>
          <label style={s.field}>
            <span style={s.label}>{t("diffField")}</span>
            <Textarea mono rows={10} value={values.diff} onChange={(v) => onEdit("diff", v)} placeholder={t("diffPlaceholder")} />
          </label>
          <span style={over ? s.counterOver : s.counter}>
            {t("charCount", { count: formatCount(values.diff.length), max: formatCount(EVAL_INPUT_DIFF_MAX_CHARS) })}
          </span>
          {over && (
            <span role="alert" style={s.invalid}>
              {t("check.diffTooLong", { max: formatCount(EVAL_INPUT_DIFF_MAX_CHARS) })}
            </span>
          )}
          {diffError && (
            <span role="alert" style={s.invalid}>
              {diffError}
            </span>
          )}
        </>
      ) : (
        <>
          <label style={s.field}>
            <span style={s.label}>{t("prTitle")}</span>
            <TextInput value={values.prTitle} onChange={(v) => onEdit("prTitle", v)} placeholder={t("titlePlaceholder")} maxLength={EVAL_PR_TITLE_MAX} />
          </label>
          <label style={s.field}>
            <span style={s.label}>{t("prDescription")}</span>
            <Textarea rows={5} value={values.prDescription} onChange={(v) => onEdit("prDescription", v)} placeholder={t("bodyPlaceholder")} />
          </label>
        </>
      )}
    </section>
  );
}
