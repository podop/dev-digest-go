"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, FormField, Icon, SearchableSelect, TextInput } from "@devdigest/ui";
import { Select } from "@/components/select";
import { useProviderModels, useSecretsStatus, useSettings, useUpdateSettings } from "@/lib/hooks";
import { toModelOptions } from "@/lib/model-label";
import type { Provider } from "@/lib/types";
// Zod-free subpath: the barrel would pull zod + every schema into /settings.
import { PROVIDER_IDS } from "@devdigest/shared/constants/feature-models";
import { s } from "./styles";

/** Provider shown before any default model is saved. */
const FALLBACK_PROVIDER: Provider = "openrouter";

/**
 * API Keys → Default model (FR8): a provider + model pair saved as
 * `settings.default_model`. Used by new agents and by features without their
 * own model. There is deliberately no control to clear it (A12). Falls back to
 * a free-text model id when the provider's model list is empty or failed.
 */
export function DefaultModelField() {
  const t = useTranslations("settingsBudget");
  const { data: settings } = useSettings();
  const { data: secrets } = useSecretsStatus();
  const update = useUpdateSettings();

  const saved = settings?.default_model ?? null;
  // The provider the user is browsing; until they pick one, the saved provider.
  const [pickedProvider, setPickedProvider] = React.useState<Provider | null>(null);
  const [typed, setTyped] = React.useState<string | null>(null);
  const provider = pickedProvider ?? saved?.provider ?? FALLBACK_PROVIDER;
  const savedModel = saved && saved.provider === provider ? saved.model : "";

  const models = useProviderModels(provider);
  const listUnavailable = models.isError || (models.data !== undefined && models.data.length === 0);
  const options = toModelOptions(models.data);
  // Keep a saved model that the provider no longer lists selectable.
  if (savedModel && !options.some((o) => (typeof o === "string" ? o : o.value) === savedModel)) {
    options.unshift(savedModel);
  }

  const typedModel = (typed ?? savedModel).trim();
  const canSaveTyped = typedModel !== "" && typedModel !== savedModel && !update.isPending;
  const providerOptions = PROVIDER_IDS.map((id) => ({ value: id, label: t(`defaultModel.providers.${id}`) }));

  const saveModel = (model: string) => update.mutate({ default_model: { provider, model } });
  const saveTyped = () => {
    if (!canSaveTyped) return;
    update.mutate({ default_model: { provider, model: typedModel } }, { onSuccess: () => setTyped(null) });
  };

  if (settings === undefined) {
    return <FormField label={t("defaultModel.label")} hint={t("defaultModel.loading")} />;
  }

  return (
    <FormField label={t("defaultModel.label")} hint={t("defaultModel.hint")}>
      <fieldset aria-label={t("defaultModel.label")} style={s.row}>
        <div style={s.provider}>
          <Select
            value={provider}
            onChange={(p) => {
              setPickedProvider(p);
              setTyped(null);
            }}
            options={providerOptions}
            aria-label={t("defaultModel.providerAria")}
            mono={false}
          />
        </div>
        <div style={s.model}>
          {listUnavailable ? (
            <TextInput
              value={typed ?? savedModel}
              onChange={setTyped}
              mono
              placeholder={saved ? t("defaultModel.pickModel") : t("defaultModel.notSet")}
              aria-label={t("defaultModel.modelAria")}
              onKeyDown={(e) => {
                if (e.key === "Enter") saveTyped();
              }}
            />
          ) : (
            <SearchableSelect
              value={savedModel}
              onChange={saveModel}
              options={options}
              placeholder={saved ? t("defaultModel.pickModel") : t("defaultModel.notSet")}
            />
          )}
        </div>
        {listUnavailable && (
          <Button kind="secondary" size="md" onClick={saveTyped} disabled={!canSaveTyped}>
            {update.isPending ? t("defaultModel.saving") : t("defaultModel.save")}
          </Button>
        )}
      </fieldset>
      {listUnavailable && (
        <div style={s.note}>
          <Icon.Info size={14} />
          <span>{t("defaultModel.freeTextHint", { provider: t(`defaultModel.providers.${provider}`) })}</span>
        </div>
      )}
      {secrets?.[provider] === false && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={14} />
          <span>{t("defaultModel.noKey", { provider: t(`defaultModel.providers.${provider}`) })}</span>
        </div>
      )}
      {update.isError && (
        <div role="alert" style={s.error}>
          {t("defaultModel.saveFailed")}
        </div>
      )}
    </FormField>
  );
}
