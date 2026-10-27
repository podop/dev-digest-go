"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { WORKSPACE_NAME_MAX, normalizeWorkspaceName, workspaceNameProblem } from "@devdigest/shared/constants/workspace";
import { Field, TextField } from "@/components/settings-kit";
import { ApiError } from "@/lib/api";
import { useRenameWorkspace, useWorkspace } from "@/lib/hooks";
import { s } from "./styles";

/**
 * Workspace → "Workspace name" (FR4): saves through PUT /workspace on Enter or blur when the value
 * changed and is valid (trimmed, 1–80 code points, no control characters). A rejected name shows
 * inline (the hook keeps the 422 out of the toast); "Saved" confirms until the next edit.
 */
export function WorkspaceNameField() {
  const t = useTranslations("settingsWorkspace");
  const errorId = React.useId();
  const { data: workspace } = useWorkspace();
  const rename = useRenameWorkspace();

  const savedName = workspace?.name ?? "";
  // null = show the saved name; a string = what the user has typed since the last save.
  const [draft, setDraft] = React.useState<string | null>(null);
  const [saved, setSaved] = React.useState(false);
  const [rejected, setRejected] = React.useState(false);

  const text = draft ?? savedName;
  const problem = draft === null ? null : workspaceNameProblem(text);
  const error = problem
    ? t(`name.errors.${problem}`, { max: WORKSPACE_NAME_MAX })
    : rejected
      ? t("name.errors.rejected")
      : null;

  const edit = (value: string) => {
    setDraft(value);
    setSaved(false);
    setRejected(false);
  };

  const commit = () => {
    if (draft === null || problem !== null || rejected || rename.isPending) return;
    const name = normalizeWorkspaceName(draft);
    if (name === savedName) {
      setDraft(null);
      return;
    }
    rename.mutate(name, {
      onSuccess: () => {
        setDraft(null);
        setSaved(true);
      },
      onError: (e) => setRejected(e instanceof ApiError && e.code === "validation_error"),
    });
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    commit();
  };

  return (
    <Field label={t("name.label")}>
      <form onSubmit={submit} noValidate>
        <TextField
          value={text}
          invalid={error !== null}
          onChange={edit}
          onBlur={commit}
          disabled={workspace === undefined}
          aria-label={t("name.inputAria")}
          aria-describedby={error ? errorId : undefined}
        />
      </form>
      {(error || rename.isPending || saved) && (
        <div style={s.notes}>
          {error && (
            <span id={errorId} role="alert" style={s.error}>
              {error}
            </span>
          )}
          {rename.isPending && <span style={s.saved}>{t("name.saving")}</span>}
          {saved && !error && (
            <span role="status" style={s.saved}>
              {t("name.saved")}
            </span>
          )}
        </div>
      )}
    </Field>
  );
}
