/* CustomProviderDialog — add / edit one custom provider (FR17, FR18, FR34). The vendored Modal gives
   role="dialog"; Escape, the focus trap and focus return come from useDialogFocus (NFR9). Secrets are
   write-only: a stored key / header value shows a placeholder and is never sent back unless retyped (FR5).
   "Save and test" saves, then runs the connection test on the saved provider and keeps the dialog open. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal, TextInput } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useSaveCustomProvider, useTestConnection } from "@/lib/hooks";
import { CUSTOM_PROVIDER_ERROR_CODES, CUSTOM_SLUG_MAX } from "@/lib/providers/custom";
import type { ConnTestResult, CustomProvider } from "@/lib/types";
import { ManualModels } from "../ManualModels";
import { AuthSection } from "./AuthSection";
import { Field, RadioGroup, fieldAria } from "./Field";
import {
  BASE_URL_MAX,
  NAME_MAX,
  buildCreate,
  buildUpdate,
  emptyForm,
  fieldErrorsFrom,
  formFromProvider,
  isValidSlug,
  localProblems,
  withName,
  withProtocol,
  type FormState,
} from "./helpers";
import { HeadersSection } from "./HeadersSection";
import { StructuredOutputField } from "./StructuredOutputField";
import { TestResultBanner } from "./TestResultBanner";
import { s } from "./styles";

const NO_ERRORS: Readonly<Record<string, string>> = {};

export function CustomProviderDialog({ provider, onClose }: { provider?: CustomProvider; onClose: () => void }) {
  const t = useTranslations("customProviders");
  const td = useTranslations("customProviders.dialog");
  const ref = useDialogFocus(onClose);
  const idBase = React.useId();
  const save = useSaveCustomProvider();
  const test = useTestConnection();

  // The provider being edited; set after "Save and test" creates one, so a second Save is a PUT, not a duplicate POST.
  const [target, setTarget] = React.useState<CustomProvider | undefined>(provider);
  const [form, setForm] = React.useState<FormState>(() => (provider ? formFromProvider(provider) : emptyForm()));
  const [errors, setErrors] = React.useState<Readonly<Record<string, string>>>(NO_ERRORS);
  const [banner, setBanner] = React.useState<string | null>(null);
  const [modeReset, setModeReset] = React.useState(false);
  const [result, setResult] = React.useState<ConnTestResult | null>(null);

  const editing = target !== undefined;
  const busy = save.isPending || test.isPending;
  const problems = localProblems(form);
  const canSubmit = problems.size === 0 && !busy;

  const clearErrors = (...paths: string[]) => {
    setBanner(null);
    if (paths.length === 0) return;
    setErrors((e) => (paths.some((p) => p in e) ? Object.fromEntries(Object.entries(e).filter(([k]) => !paths.includes(k))) : e));
  };
  const clearIndexedErrors = (prefix: string) =>
    setErrors((e) => (Object.keys(e).some((k) => k.startsWith(prefix)) ? Object.fromEntries(Object.entries(e).filter(([k]) => !k.startsWith(prefix))) : e));
  const patch = (changes: Partial<FormState>, ...clear: string[]) => {
    setForm((f) => ({ ...f, ...changes }));
    clearErrors(...clear);
  };

  const showSaveError = (e: unknown) => {
    if (!(e instanceof ApiError)) return setBanner(td("saveFailed"));
    if (e.code === "validation_error") {
      const { fields, rest } = fieldErrorsFrom(e.details);
      setErrors(fields);
      if (rest.length > 0 || Object.keys(fields).length === 0) setBanner(rest[0] ?? e.message);
    } else if (e.code === CUSTOM_PROVIDER_ERROR_CODES.exists) {
      setErrors({ id: e.message });
    } else if (e.code === CUSTOM_PROVIDER_ERROR_CODES.limit) {
      setBanner(e.message || td("limitReached"));
    } else {
      setBanner(e.message || td("saveFailed"));
    }
  };

  const submit = async (andTest: boolean) => {
    if (!canSubmit) return;
    setErrors(NO_ERRORS);
    setBanner(null);
    setResult(null);
    let saved: CustomProvider;
    try {
      saved = await save.mutateAsync(
        target
          ? { mode: "update", id: target.id, input: buildUpdate(form, true) }
          : { mode: "create", input: buildCreate(form) },
      );
    } catch (e) {
      showSaveError(e);
      return;
    }
    if (!andTest) return onClose();
    setTarget(saved);
    setForm(formFromProvider(saved));
    try {
      setResult(await test.mutateAsync(saved.id));
    } catch (e) {
      setResult({ provider: saved.id, ok: false, message: e instanceof ApiError ? e.message : t("test.failed") });
    }
  };

  const ids = { name: `${idBase}-name`, slug: `${idBase}-slug`, url: `${idBase}-url` };
  const slugError = errors.id ?? (form.slug !== "" && !isValidSlug(form.slug) ? td("slugInvalid") : undefined);

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={680}
        title={target ? td("editTitle", { name: target.name }) : td("addTitle")}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={onClose}>
              {td("cancel")}
            </Button>
            <Button type="button" kind="secondary" onClick={() => void submit(true)} disabled={!canSubmit}>
              {test.isPending ? td("testing") : td("saveAndTest")}
            </Button>
            <Button type="button" kind="primary" onClick={() => void submit(false)} disabled={!canSubmit}>
              {save.isPending ? td("saving") : td("save")}
            </Button>
          </div>
        }
      >
        <div style={s.body}>
          <Field id={ids.name} label={td("name")} error={errors.name}>
            <TextInput
              {...fieldAria(ids.name, errors.name)}
              value={form.name}
              onChange={(name) => {
                setForm((f) => withName(f, name));
                clearErrors("name");
              }}
              maxLength={NAME_MAX}
              autoComplete="off"
            />
          </Field>
          <Field
            id={ids.slug}
            label={td("slug")}
            hint={editing ? td("slugHintEdit") : td("slugHint", { slug: form.slug || "…" })}
            error={slugError}
          >
            <TextInput
              {...fieldAria(ids.slug, slugError, true)}
              mono
              value={form.slug}
              onChange={(slug) => patch({ slug, slugTouched: true }, "id")}
              maxLength={CUSTOM_SLUG_MAX}
              readOnly={editing}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <RadioGroup
            legend={td("protocol")}
            name={`${idBase}-protocol`}
            value={form.protocol}
            options={[
              { value: "openai", label: t("protocol.openai") },
              { value: "anthropic", label: t("protocol.anthropic") },
            ]}
            onChange={(protocol) => {
              const next = withProtocol(form, protocol);
              setForm(next.form);
              setModeReset(next.modeReset);
              clearErrors("protocol", "auth_mode", "auth_header", "structured_output");
            }}
          />
          <Field id={ids.url} label={td("baseUrl")} hint={td("baseUrlHint")} error={errors.base_url}>
            <TextInput
              {...fieldAria(ids.url, errors.base_url, true)}
              mono
              value={form.baseUrl}
              onChange={(baseUrl) => patch({ baseUrl }, "base_url")}
              maxLength={BASE_URL_MAX}
              placeholder="https://gateway.example.com/v1"
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <AuthSection form={form} patch={patch} errors={errors} keyConfigured={target?.key_configured ?? false} idBase={idBase} />
          <HeadersSection
            headers={form.headers}
            onChange={(headers) => setForm((f) => ({ ...f, headers }))}
            errors={errors}
            onErrorsChange={() => clearIndexedErrors("extra_headers")}
            idBase={idBase}
          />
          <StructuredOutputField
            value={form.structured}
            protocol={form.protocol}
            onChange={(structured) => {
              setModeReset(false);
              patch({ structured }, "structured_output");
            }}
            modeReset={modeReset}
            error={errors.structured_output}
            idBase={idBase}
          />
          <ManualModels
            models={form.models}
            onChange={(models) => setForm((f) => ({ ...f, models }))}
            serverErrors={errors}
            onServerErrorsChange={() => clearIndexedErrors("models")}
          />
          {banner && (
            <div role="alert" style={s.alert}>
              {banner}
            </div>
          )}
          {result && !test.isPending && <TestResultBanner result={result} name={target?.name ?? form.name} />}
        </div>
      </Modal>
    </div>
  );
}
