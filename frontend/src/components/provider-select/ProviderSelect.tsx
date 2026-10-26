/* ProviderSelect — the one provider picker of the UI (custom model providers, FR19, SCR6): OpenRouter, OpenAI,
   Anthropic, then the custom providers by name, built on the themed Select. A stored custom id that no provider
   defines is listed as "<id> · Not configured" so it stays visible; the parent blocks Save with
   `useProviderUndefined`. While the custom list loads only the built-ins are offered; if it failed, an error
   line with Retry follows the picker. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { Select } from "@/components/select";
import { useCustomProviders } from "@/lib/hooks";
import type { Provider } from "@/lib/types";
import { BUILT_IN_PROVIDER_IDS, isCustomId, isUndefinedCustomId, providerOptions } from "@/lib/providers/custom";
import { s } from "./styles";

/** Narrows an option value (always one of the listed ids) back to `Provider`. */
function asProvider(v: string): Provider | null {
  const builtIn = BUILT_IN_PROVIDER_IDS.find((id) => id === v);
  if (builtIn) return builtIn;
  return isCustomId(v) ? v : null;
}

export function ProviderSelect({
  value,
  onChange,
  "aria-label": ariaLabel,
  mono = true,
  disabled,
}: {
  value: Provider;
  onChange: (provider: Provider) => void;
  "aria-label": string;
  mono?: boolean;
  disabled?: boolean;
}) {
  const t = useTranslations("providerSelect");
  const { data: custom, isError, refetch } = useCustomProviders();

  const options = React.useMemo(() => {
    const list = providerOptions(custom, {
      openrouter: t("builtIn.openrouter"),
      openai: t("builtIn.openai"),
      anthropic: t("builtIn.anthropic"),
    });
    if (list.some((o) => o.value === value)) return list;
    // A stored custom id the list does not (yet) contain stays visible.
    const label = isUndefinedCustomId(value, custom) && custom !== undefined ? t("notConfigured", { id: value }) : value;
    return [...list, { value, label, custom: true }];
  }, [custom, value, t]);

  return (
    <div style={s.wrap}>
      <Select
        value={value as string}
        onChange={(v) => {
          const next = asProvider(v);
          if (next) onChange(next);
        }}
        options={options}
        aria-label={ariaLabel}
        mono={mono}
        disabled={disabled}
      />
      {isError && (
        <div aria-live="polite" style={s.error}>
          <span>{t("loadFailed")}</span>
          <Button kind="secondary" size="sm" onClick={() => refetch()}>
            {t("retry")}
          </Button>
        </div>
      )}
    </div>
  );
}
