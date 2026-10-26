/* ConnectionDialog — add / edit one GitLab connection (FR35). The vendored Modal gives role="dialog"; Escape, the
   focus trap and focus return come from useDialogFocus. The token is write-only: a stored one shows a placeholder and
   is only sent when retyped; "Remove token" sends null. "Save and test" saves, then tests the saved connection and
   keeps the dialog open with the result; the dialog then edits the saved connection (a second Save is a PUT). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Icon, Modal, TextInput } from "@devdigest/ui";
import { applyMaskedEdit, maskKey } from "@/components/key-field";
import { ApiError } from "@/lib/api";
import { GITLAB_ERROR_CODES } from "@/lib/code-host";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useSaveGitLabConnection, useTestConnection } from "@/lib/hooks";
import type { ConnTestResult, GitLabConnection } from "@/lib/types";
import {
  BASE_URL_MAX,
  NAME_MAX,
  buildCreate,
  buildUpdate,
  canSubmit,
  createTokenUrl,
  emptyForm,
  fieldErrorsFrom,
  formFromConnection,
  hostOfBaseUrl,
  type FormState,
} from "../../helpers";
import { TestResult } from "../TestResult";
import { Field, fieldAria } from "./Field";
import { s } from "./styles";

const NO_ERRORS: Readonly<Record<string, string>> = {};

interface Props {
  connection?: GitLabConnection;
  onClose: () => void;
  /** A result produced by "Save and test", so the list shows it too. */
  onTested: (id: string, result: ConnTestResult) => void;
}

export function ConnectionDialog({ connection, onClose, onTested }: Props) {
  const t = useTranslations("gitlabIntegration.dialog");
  const ref = useDialogFocus(onClose);
  const idBase = React.useId();
  const save = useSaveGitLabConnection();
  const test = useTestConnection();

  // The connection being edited; set after "Save and test" creates one, so a second Save is a PUT, not a duplicate POST.
  const [target, setTarget] = React.useState<GitLabConnection | undefined>(connection);
  const [form, setForm] = React.useState<FormState>(() => (connection ? formFromConnection(connection) : emptyForm()));
  const [errors, setErrors] = React.useState<Readonly<Record<string, string>>>(NO_ERRORS);
  const [banner, setBanner] = React.useState<string | null>(null);
  const [result, setResult] = React.useState<ConnTestResult | null>(null);
  const [reveal, setReveal] = React.useState(false);

  const busy = save.isPending || test.isPending;
  const submittable = canSubmit(form) && !busy;
  const tokenSaved = target?.token_configured ?? false;
  const tokenUrl = createTokenUrl(form.baseUrl);

  const patch = (changes: Partial<FormState>, ...clear: string[]) => {
    setForm((f) => ({ ...f, ...changes }));
    setBanner(null);
    if (clear.some((k) => k in errors)) setErrors(Object.fromEntries(Object.entries(errors).filter(([k]) => !clear.includes(k))));
  };

  const showSaveError = (e: unknown) => {
    if (!(e instanceof ApiError)) return setBanner(t("saveFailed"));
    if (e.code === "validation_error") {
      const { fields, rest } = fieldErrorsFrom(e.details);
      setErrors(fields);
      if (rest.length > 0 || Object.keys(fields).length === 0) setBanner(rest[0] ?? e.message);
    } else if (e.code === GITLAB_ERROR_CODES.connectionExists) {
      setErrors({ base_url: e.message });
    } else if (e.code === GITLAB_ERROR_CODES.connectionLimit) {
      setBanner(e.message || t("limitReached"));
    } else {
      setBanner(e.message || t("saveFailed"));
    }
  };

  const submit = async (andTest: boolean) => {
    if (!submittable) return;
    setErrors(NO_ERRORS);
    setBanner(null);
    setResult(null);
    let saved: GitLabConnection;
    try {
      saved = await save.mutateAsync(
        target
          ? { mode: "update", id: target.id, input: buildUpdate(form, target) }
          : { mode: "create", input: buildCreate(form) },
      );
    } catch (e) {
      showSaveError(e);
      return;
    }
    if (!andTest) return onClose();
    setTarget(saved);
    setForm(formFromConnection(saved));
    let next: ConnTestResult;
    try {
      next = await test.mutateAsync({ provider: "gitlab", connection_id: saved.id });
    } catch (e) {
      next = { provider: "gitlab", ok: false, message: e instanceof ApiError ? e.message : t("saveFailed") };
    }
    setResult(next);
    onTested(saved.id, next);
  };

  const ids = { name: `${idBase}-name`, url: `${idBase}-url`, token: `${idBase}-token` };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={560}
        title={target ? t("editTitle", { name: target.name }) : t("addTitle")}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={onClose}>
              {t("cancel")}
            </Button>
            <Button type="button" kind="secondary" onClick={() => void submit(true)} disabled={!submittable}>
              {test.isPending ? t("testing") : t("saveAndTest")}
            </Button>
            <Button type="button" kind="primary" onClick={() => void submit(false)} disabled={!submittable}>
              {save.isPending ? t("saving") : t("save")}
            </Button>
          </div>
        }
      >
        <div style={s.body}>
          <Field id={ids.name} label={t("name")} error={errors.name}>
            <TextInput
              {...fieldAria(ids.name, errors.name)}
              value={form.name}
              onChange={(name) => patch({ name }, "name")}
              placeholder={hostOfBaseUrl(form.baseUrl) ?? t("namePlaceholder")}
              maxLength={NAME_MAX}
              autoComplete="off"
            />
          </Field>
          <Field id={ids.url} label={t("baseUrl")} hint={t("baseUrlHint")} error={errors.base_url}>
            <TextInput
              {...fieldAria(ids.url, errors.base_url, true)}
              mono
              value={form.baseUrl}
              onChange={(baseUrl) => patch({ baseUrl }, "base_url")}
              maxLength={BASE_URL_MAX}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <Field
            id={ids.token}
            label={t("token")}
            hint={t("tokenHint")}
            error={errors.token}
            aside={
              tokenUrl && (
                <a href={tokenUrl} target="_blank" rel="noopener noreferrer" style={s.link}>
                  {t("createToken")}
                  <Icon.ExternalLink size={12} />
                </a>
              )
            }
          >
            <TextInput
              {...fieldAria(ids.token, errors.token, true)}
              mono
              value={reveal ? form.token : maskKey(form.token)}
              onChange={(v) => patch({ token: reveal ? v : applyMaskedEdit(form.token, v), removeToken: false }, "token")}
              placeholder={tokenSaved && !form.removeToken ? t("tokenSaved") : t("tokenPlaceholder")}
              autoComplete="off"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-1p-ignore
              data-lpignore="true"
              suffix={
                <button
                  type="button"
                  style={s.toggle}
                  aria-label={t("reveal")}
                  aria-pressed={reveal}
                  onClick={() => setReveal((r) => !r)}
                >
                  {reveal ? <Icon.Eye size={14} /> : <Icon.EyeOff size={14} />}
                </button>
              }
            />
          </Field>
          {tokenSaved && (
            <label style={s.check}>
              <input
                type="checkbox"
                checked={form.removeToken}
                onChange={(e) => patch({ removeToken: e.target.checked, ...(e.target.checked ? { token: "" } : {}) })}
              />
              {t("removeToken")}
            </label>
          )}
          {banner && (
            <div role="alert" style={s.alert}>
              {banner}
            </div>
          )}
          {result && !test.isPending && <TestResult result={result} />}
        </div>
      </Modal>
    </div>
  );
}
