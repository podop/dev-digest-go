/* AuthSection — auth mode, header name and the write-only key of the custom provider dialog (FR4, FR5, FR17).
   The key is masked like Settings A's key fields (FR3); a stored key shows only its placeholder. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon, TextInput } from "@devdigest/ui";
import { applyMaskedEdit, maskKey } from "@/components/key-field";
import { Field, RadioGroup, fieldAria } from "./Field";
import type { FormState } from "./helpers";
import { s } from "./styles";

export function AuthSection({
  form,
  patch,
  errors,
  keyConfigured,
  idBase,
}: {
  form: FormState;
  /** Update fields and drop the 422 messages of the listed paths. */
  patch: (changes: Partial<FormState>, ...clear: string[]) => void;
  errors: Readonly<Record<string, string>>;
  /** Edit only: a key is stored. */
  keyConfigured: boolean;
  idBase: string;
}) {
  const t = useTranslations("customProviders.dialog");
  const [reveal, setReveal] = React.useState(false);
  const keyId = `${idBase}-key`;
  const headerId = `${idBase}-auth-header`;

  return (
    <>
      <RadioGroup
        legend={t("authMode")}
        name={`${idBase}-auth-mode`}
        value={form.authMode}
        options={[
          { value: "bearer", label: t("authBearer") },
          { value: "header", label: t("authHeader") },
        ]}
        onChange={(authMode) => patch({ authMode, authTouched: true }, "auth_mode", "auth_header")}
      />
      {form.authMode === "header" && (
        <Field id={headerId} label={t("authHeaderName")} error={errors.auth_header}>
          <TextInput
            {...fieldAria(headerId, errors.auth_header)}
            mono
            value={form.authHeader}
            onChange={(authHeader) => patch({ authHeader, authTouched: true }, "auth_header")}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
      )}
      <Field id={keyId} label={t("apiKey")} hint={t("apiKeyHint")} error={errors.api_key}>
        <TextInput
          {...fieldAria(keyId, errors.api_key, true)}
          mono
          value={reveal ? form.apiKey : maskKey(form.apiKey)}
          onChange={(v) => patch({ apiKey: reveal ? v : applyMaskedEdit(form.apiKey, v) }, "api_key")}
          placeholder={keyConfigured && !form.removeKey ? t("apiKeySaved") : t("apiKeyPlaceholder")}
          disabled={form.removeKey}
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
              aria-label={t("revealKey")}
              aria-pressed={reveal}
              onClick={() => setReveal((r) => !r)}
            >
              {reveal ? <Icon.Eye size={14} /> : <Icon.EyeOff size={14} />}
            </button>
          }
        />
      </Field>
      {keyConfigured && (
        <label style={s.check}>
          <input
            type="checkbox"
            checked={form.removeKey}
            onChange={(e) => patch({ removeKey: e.target.checked, apiKey: "" }, "api_key")}
          />
          {t("removeKey")}
        </label>
      )}
      {form.removeKey && (
        <div role="status" style={s.note}>
          {t("keyWillBeRemoved")}
        </div>
      )}
    </>
  );
}
