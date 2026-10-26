"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, FormField, Icon, SearchableSelect, TextInput } from "@devdigest/ui";
import { ProviderSelect, useProviderUndefined } from "@/components/provider-select";
import { useCustomProviders, useProviderModels, useSecretsStatus, useSettings, useUpdateSettings } from "@/lib/hooks";
import { toModelOptions } from "@/lib/model-label";
import { isCustomId, providerName } from "@/lib/providers/custom";
import type { Provider } from "@/lib/types";
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
  const tp = useTranslations("providerSelect");
  const { data: settings } = useSettings();
  const { data: secrets } = useSecretsStatus();
  const { data: custom } = useCustomProviders();
  const update = useUpdateSettings();

  const saved = settings?.default_model ?? null;
  // The provider the user is browsing; until they pick one, the saved provider.
  const [pickedProvider, setPickedProvider] = React.useState<Provider | null>(null);
  const [typed, setTyped] = React.useState<string | null>(null);
  const provider = pickedProvider ?? saved?.provider ?? FALLBACK_PROVIDER;
  const savedModel = saved && saved.provider === provider ? saved.model : "";

  // A saved custom provider that no longer exists: nothing can be saved until another one is picked (FR19).
  const providerUndefined = useProviderUndefined(provider);
  const shownName = providerName(provider, custom);

  const models = useProviderModels(provider);
  const listUnavailable = models.isError || (models.data !== undefined && models.data.length === 0);
  const options = toModelOptions(models.data);
  // Keep a saved model that the provider no longer lists selectable.
  if (savedModel && !options.some((o) => (typeof o === "string" ? o : o.value) === savedModel)) {
    options.unshift(savedModel);
  }

  const typedModel = (typed ?? savedModel).trim();
  const canSaveTyped = typedModel !== "" && typedModel !== savedModel && !update.isPending && !providerUndefined;

  const saveModel = (model: string) => {
    if (providerUndefined) return;
    update.mutate({ default_model: { provider, model } });
  };
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
          <ProviderSelect
            value={provider}
            onChange={(p) => {
              setPickedProvider(p);
              setTyped(null);
            }}
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
          <span>{t("defaultModel.freeTextHint", { provider: shownName })}</span>
        </div>
      )}
      {providerUndefined && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={14} />
          <span>{tp("blockedHint")}</span>
        </div>
      )}
      {!isCustomId(provider) && secrets?.[provider] === false && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={14} />
          <span>{t("defaultModel.noKey", { provider: shownName })}</span>
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
