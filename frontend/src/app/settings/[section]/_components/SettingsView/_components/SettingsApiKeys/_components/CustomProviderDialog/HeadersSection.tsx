/* HeadersSection — up to five extra request headers (FR4, FR17). A value is write-only: a stored one shows
   "Value saved" and, left untouched, is sent as a name alone so the server keeps it (FR5). */
"use client";

import { useTranslations } from "next-intl";
import { Button, IconBtn, TextInput } from "@devdigest/ui";
import { fieldAria } from "./Field";
import { MAX_HEADERS, keepsStoredValue, newHeader, type HeaderDraft } from "./helpers";
import { s } from "./styles";

export function HeadersSection({
  headers,
  onChange,
  errors,
  onErrorsChange,
  idBase,
}: {
  headers: readonly HeaderDraft[];
  onChange: (next: HeaderDraft[]) => void;
  errors: Readonly<Record<string, string>>;
  /** Called on row changes so index-based 422 messages are dropped. */
  onErrorsChange: () => void;
  idBase: string;
}) {
  const t = useTranslations("customProviders.dialog");
  const edit = (index: number, changes: Partial<HeaderDraft>) => {
    onErrorsChange();
    onChange(headers.map((h, i) => (i === index ? { ...h, ...changes } : h)));
  };

  return (
    <section style={s.section} aria-labelledby={`${idBase}-headers-title`}>
      <div>
        <h3 id={`${idBase}-headers-title`} style={s.label}>
          {t("headers")}
        </h3>
        <div style={s.hint}>{t("headersHint")}</div>
      </div>
      {headers.map((h, i) => {
        const n = i + 1;
        const nameError = errors[`extra_headers.${i}.name`] ?? errors[`extra_headers.${i}`];
        const valueError = errors[`extra_headers.${i}.value`];
        const nameId = `${idBase}-${h.key}-name`;
        const valueId = `${idBase}-${h.key}-value`;
        return (
          <div key={h.key} style={s.row}>
            <div style={s.grow}>
              <TextInput
                {...fieldAria(nameId, nameError)}
                mono
                value={h.name}
                onChange={(name) => edit(i, { name })}
                placeholder={t("headerName", { n })}
                aria-label={t("headerName", { n })}
                autoComplete="off"
                spellCheck={false}
              />
              {nameError && (
                <div id={`${nameId}-error`} style={s.error}>
                  {nameError}
                </div>
              )}
            </div>
            <div style={s.grow}>
              <TextInput
                {...fieldAria(valueId, valueError)}
                type="password"
                mono
                value={h.value}
                onChange={(value) => edit(i, { value })}
                placeholder={keepsStoredValue(h) ? t("headerValueSaved") : t("headerValuePlaceholder")}
                aria-label={t("headerValue", { n })}
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
              />
              {valueError && (
                <div id={`${valueId}-error`} style={s.error}>
                  {valueError}
                </div>
              )}
            </div>
            <IconBtn
              icon="X"
              label={t("removeHeader", { n })}
              onClick={() => {
                onErrorsChange();
                onChange(headers.filter((_, j) => j !== i));
              }}
            />
          </div>
        );
      })}
      <div>
        <Button
          type="button"
          kind="secondary"
          size="sm"
          icon="Plus"
          disabled={headers.length >= MAX_HEADERS}
          onClick={() => onChange([...headers, newHeader()])}
        >
          {t("addHeader")}
        </Button>
      </div>
    </section>
  );
}
