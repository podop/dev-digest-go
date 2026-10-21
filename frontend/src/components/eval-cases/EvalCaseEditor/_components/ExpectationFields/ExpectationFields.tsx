/* ExpectationFields — what the agent must find / must not flag: kind, file, new-side lines,
   severity, category, title. The file is picked from the diff's files when the paste has
   file headers, typed otherwise (FR10). Inline checks and a server rejection show at the
   field they belong to (FR11). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { TextInput } from "@devdigest/ui";
import { EVAL_EXPECTATION_FILE_MAX, EVAL_EXPECTATION_TITLE_MAX } from "@devdigest/shared/constants/evals";
import { Select, type SelectOption } from "@/components/select";
import { CATEGORIES, KINDS, SEVERITIES } from "../../constants";
import { formatRanges, type CaseValidity, type DiffAnalysis, type EditorValues, type SaveError } from "../../helpers";
import { s } from "../../styles";

export function ExpectationFields({
  values,
  validity,
  analysis,
  failure,
  onEdit,
}: {
  values: EditorValues;
  validity: CaseValidity;
  analysis: DiffAnalysis;
  failure: SaveError | null;
  onEdit: <K extends keyof EditorValues>(field: K, value: EditorValues[K]) => void;
}) {
  const t = useTranslations("eval.caseEditor");

  const kindOptions: SelectOption<EditorValues["kind"]>[] = KINDS.map((k) => ({ value: k, label: t(`kindValue.${k}`) }));
  const severityOptions: SelectOption<EditorValues["severity"]>[] = [
    { value: "", label: t("none") },
    ...SEVERITIES.map((v) => ({ value: v, label: t(`severityValue.${v}`) })),
  ];
  const categoryOptions: SelectOption<EditorValues["category"]>[] = [
    { value: "", label: t("none") },
    ...CATEGORIES.map((v) => ({ value: v, label: t(`categoryValue.${v}`) })),
  ];

  // File Select: the diff's files; the current value stays selectable so a stale one is visible and flagged.
  const paths = analysis.hasHeaders && analysis.paths.length > 0 ? analysis.paths : null;
  const fileOptions = paths ? (values.file === "" || paths.includes(values.file) ? paths : [values.file, ...paths]) : null;

  const fileFiles = failure?.field === "file" ? (failure.files ?? []) : [];
  const fileError = validity.fileNotInDiff
    ? t("check.fileNotInDiff", { files: (paths ?? []).join(", ") })
    : failure?.field === "file"
      ? t("error.fileNotInDiff", { files: fileFiles.join(", ") })
      : null;

  const linesError = validity.linesInvalid
    ? t("check.linesInvalid")
    : validity.rangeInverted
      ? t("check.linesInverted")
      : validity.linesOutside
        ? t("check.linesOutside", { ranges: formatRanges(validity.linesOutside) })
        : failure?.field === "lines"
          ? t("error.linesOutside", { ranges: formatRanges(failure.ranges ?? []) })
          : null;

  return (
    <section aria-label={t("expectationHeading")} style={s.section}>
      <h3 style={s.heading}>{t("expectationHeading")}</h3>
      <div style={s.row}>
        <div style={s.field}>
          <span style={s.label}>{t("kind")}</span>
          <Select aria-label={t("kind")} mono={false} value={values.kind} options={kindOptions} onChange={(v) => onEdit("kind", v)} />
        </div>
        <div style={s.field}>
          <span style={s.label}>{t("file")}</span>
          {fileOptions ? (
            <Select aria-label={t("file")} value={values.file} options={fileOptions} onChange={(v) => onEdit("file", v)} />
          ) : (
            <TextInput aria-label={t("file")} mono value={values.file} onChange={(v) => onEdit("file", v)} placeholder={t("filePlaceholder")} maxLength={EVAL_EXPECTATION_FILE_MAX} />
          )}
        </div>
      </div>
      {fileError && (
        <span role="alert" style={s.invalid}>
          {fileError}
        </span>
      )}
      <div style={s.row}>
        <label style={s.field}>
          <span style={s.label}>{t("startLine")}</span>
          <TextInput type="number" min={1} value={values.startLine} onChange={(v) => onEdit("startLine", v)} />
        </label>
        <label style={s.field}>
          <span style={s.label}>{t("endLine")}</span>
          <TextInput type="number" min={1} value={values.endLine} onChange={(v) => onEdit("endLine", v)} />
        </label>
      </div>
      {linesError && (
        <span role="alert" style={s.invalid}>
          {linesError}
        </span>
      )}
      <div style={s.row}>
        <div style={s.field}>
          <span style={s.label}>{t("severity")}</span>
          <Select aria-label={t("severity")} mono={false} value={values.severity} options={severityOptions} onChange={(v) => onEdit("severity", v)} />
        </div>
        <div style={s.field}>
          <span style={s.label}>{t("category")}</span>
          <Select aria-label={t("category")} mono={false} value={values.category} options={categoryOptions} onChange={(v) => onEdit("category", v)} />
        </div>
      </div>
      <label style={s.field}>
        <span style={s.label}>{t("findingTitle")}</span>
        <TextInput value={values.title} onChange={(v) => onEdit("title", v)} maxLength={EVAL_EXPECTATION_TITLE_MAX} />
      </label>
    </section>
  );
}
