/* ManualModels — the dialog's "Models" section (FR18): models the gateway does not list, with optional
   prices per 1M tokens. Controlled: the dialog owns the rows. Prices stay text; a bad price or a half-filled
   pair is shown inline and blocks Save (AC23). `serverErrors` are the 422 messages by path ("models.0.id"). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, IconBtn, TextInput } from "@devdigest/ui";
import { MAX_MODELS, MODEL_ID_MAX, MODEL_LABEL_MAX } from "./constants";
import { modelProblems, newModelDraft, type ModelDraft, type ModelProblem } from "./helpers";
import { s } from "./styles";

type Column = "id" | "label" | "prompt" | "completion";

export function ManualModels({
  models,
  onChange,
  serverErrors,
  onServerErrorsChange,
}: {
  models: readonly ModelDraft[];
  onChange: (next: ModelDraft[]) => void;
  serverErrors: Readonly<Record<string, string>>;
  /** Called when the user edits or removes rows, so stale index-based 422 messages are dropped. */
  onServerErrorsChange: () => void;
}) {
  const t = useTranslations("customProviders.models");
  const base = React.useId();
  const problems = React.useMemo(() => modelProblems(models), [models]);

  const edit = (index: number, column: Column, value: string) => {
    onServerErrorsChange();
    onChange(models.map((m, i) => (i === index ? { ...m, [column]: value } : m)));
  };
  const remove = (index: number) => {
    onServerErrorsChange();
    onChange(models.filter((_, i) => i !== index));
  };

  /** The server's 422 message for one cell (paths from the API: models.<i>.id, .label, .pricing[.promptPerM]). */
  const serverError = (index: number, column: Column): string | undefined => {
    if (column === "id" || column === "label") return serverErrors[`models.${index}.${column}`];
    const field = column === "prompt" ? "promptPerM" : "completionPerM";
    return serverErrors[`models.${index}.pricing.${field}`] ?? serverErrors[`models.${index}.pricing`];
  };
  /** Inline message of one cell: a local problem (not "required" on an untouched empty id) or the server's message. */
  const errorOf = (index: number, column: Column, problem?: ModelProblem): string | undefined => {
    const untouched = problem === "idRequired" && models[index]?.id === "";
    return problem && !untouched ? t(problem) : serverError(index, column);
  };

  return (
    <section style={s.wrap} aria-labelledby={`${base}-title`}>
      <div style={s.head}>
        <h3 id={`${base}-title`} style={s.title}>
          {t("title")}
        </h3>
      </div>
      <p style={s.hint}>{t("hint")}</p>
      {models.length === 0 ? (
        <p style={s.empty}>{t("empty")}</p>
      ) : (
        <ul style={s.list}>
          {models.map((m, i) => {
            const n = i + 1;
            const rowProblems = problems[i] ?? {};
            const idError = errorOf(i, "id", rowProblems.id);
            const promptError = errorOf(i, "prompt", rowProblems.prompt);
            const completionError = errorOf(i, "completion", rowProblems.completion);
            const cell = (column: Column, error: string | undefined, props: { mono?: boolean; max?: number }) => {
              const id = `${base}-${m.key}-${column}`;
              return (
                <>
                  <TextInput
                    id={id}
                    mono={props.mono}
                    value={m[column]}
                    onChange={(v) => edit(i, column, v)}
                    placeholder={t(`${column}Placeholder`)}
                    aria-label={t(column, { n })}
                    aria-invalid={error ? true : undefined}
                    aria-describedby={error ? `${id}-error` : undefined}
                    maxLength={props.max}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {error && (
                    <div id={`${id}-error`} style={s.error}>
                      {error}
                    </div>
                  )}
                </>
              );
            };
            return (
              <li key={m.key} style={s.row}>
                <div style={s.cell}>{cell("id", idError, { mono: true, max: MODEL_ID_MAX })}</div>
                <div style={s.cell}>{cell("label", errorOf(i, "label"), { max: MODEL_LABEL_MAX })}</div>
                <div style={s.price}>{cell("prompt", promptError, { mono: true })}</div>
                <div style={s.price}>{cell("completion", completionError, { mono: true })}</div>
                <IconBtn icon="X" label={t("remove", { n })} onClick={() => remove(i)} />
              </li>
            );
          })}
        </ul>
      )}
      <Button
        type="button"
        kind="secondary"
        size="sm"
        icon="Plus"
        style={s.add}
        disabled={models.length >= MAX_MODELS}
        onClick={() => onChange([...models, newModelDraft()])}
      >
        {t("add")}
      </Button>
    </section>
  );
}
