/* StructuredOutputField — Strict / JSON object / Prompt only (FR34). JSON object is offered only for an
   OpenAI-compatible gateway; the dialog says so when switching the protocol reset it. */
"use client";

import { useTranslations } from "next-intl";
import type { CustomProviderProtocol, CustomProviderStructuredOutput } from "@/lib/types";
import { RadioGroup, type RadioOption } from "./Field";
import { s } from "./styles";

export function StructuredOutputField({
  value,
  protocol,
  onChange,
  modeReset,
  error,
  idBase,
}: {
  value: CustomProviderStructuredOutput;
  protocol: CustomProviderProtocol;
  onChange: (v: CustomProviderStructuredOutput) => void;
  /** The last protocol switch fell back from JSON object to Strict. */
  modeReset: boolean;
  error?: string;
  idBase: string;
}) {
  const t = useTranslations("customProviders");
  const options: RadioOption<CustomProviderStructuredOutput>[] = [
    { value: "strict", label: t("modes.strict") },
    ...(protocol === "openai" ? [{ value: "json_object" as const, label: t("modes.json_object") }] : []),
    { value: "prompt_only", label: t("modes.prompt_only") },
  ];
  const helpId = `${idBase}-structured-help`;
  return (
    <div style={s.section}>
      <RadioGroup
        legend={t("dialog.structuredOutput")}
        name={`${idBase}-structured`}
        value={value}
        options={options}
        onChange={onChange}
        describedBy={helpId}
      />
      <div id={helpId} style={s.hint}>
        {t("dialog.structuredHelp")}
      </div>
      {modeReset && (
        <div role="status" style={s.note}>
          {t("dialog.jsonObjectReset")}
        </div>
      )}
      {error && <div style={s.error}>{error}</div>}
    </div>
  );
}
