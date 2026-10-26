"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { FormField, SearchableSelect } from "@devdigest/ui";
import type { FeatureModelChoice } from "@devdigest/shared";
import type { FeatureModelDef } from "@devdigest/shared/constants/feature-models";
import { ProviderSelect, useProviderUndefined } from "@/components/provider-select";
import { useProviderModels } from "@/lib/hooks";
import { toModelOptions } from "@/lib/model-label";
import type { Provider } from "@/lib/types";
import { s } from "./styles";

/** Provider of a feature without its own choice: the picker has always listed OpenRouter models for it. */
const FALLBACK_PROVIDER: Provider = "openrouter";

/**
 * One Feature Models row: a provider choice (FR19) and the model picker of that provider. The row persists
 * `{provider, model}` when a model is picked, so picking another provider only changes the list until a model of
 * it is chosen. A stored custom provider that no longer exists blocks the model picker ("Not configured").
 */
export function FeatureModelRow({
  feature,
  own,
  defaultModel,
  onPick,
}: {
  feature: FeatureModelDef;
  own: FeatureModelChoice | undefined;
  defaultModel: FeatureModelChoice | null;
  onPick: (choice: FeatureModelChoice) => void;
}) {
  const t = useTranslations("settings");
  const tp = useTranslations("providerSelect");
  const [pickedProvider, setPickedProvider] = React.useState<Provider | null>(null);
  const stored: Provider = own?.provider ?? FALLBACK_PROVIDER;
  const provider = pickedProvider ?? stored;
  const providerUndefined = useProviderUndefined(provider);
  const { data: models } = useProviderModels(provider);

  // The model shown is the effective one while the stored provider is browsed; another provider starts empty.
  const current = provider === stored ? (own?.model ?? defaultModel?.model ?? feature.defaultModel) : "";
  const options = toModelOptions(models);
  // Keep a saved model that the provider's live list lacks selectable.
  if (current && !options.some((o) => (typeof o === "string" ? o : o.value) === current)) options.unshift(current);

  return (
    <div style={s.row}>
      <FormField
        label={
          <>
            {feature.label}
            {!own && (
              <span style={s.defaultTag}>
                {defaultModel
                  ? t("models.usingDefaultModel", { model: `${defaultModel.provider}/${defaultModel.model}` })
                  : t("models.usingDefault")}
              </span>
            )}
          </>
        }
        hint={feature.description}
      >
        <div style={s.pickers}>
          <div style={s.provider}>
            <ProviderSelect
              value={provider}
              onChange={setPickedProvider}
              aria-label={t("models.providerAria", { feature: feature.label })}
              mono={false}
            />
          </div>
          <div style={s.model}>
            {providerUndefined ? (
              <span role="note" style={s.blocked}>
                {tp("blockedHint")}
              </span>
            ) : (
              <SearchableSelect
                value={current}
                onChange={(model) => {
                  onPick({ provider, model });
                  setPickedProvider(null);
                }}
                options={options}
                placeholder={t("models.search")}
              />
            )}
          </div>
        </div>
      </FormField>
    </div>
  );
}
