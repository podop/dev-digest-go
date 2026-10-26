"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { useSettings, useUpdateSettings } from "@/lib/hooks";
import { useProviderModels } from "@/lib/hooks";
import type { FeatureModelChoice, FeatureModelId } from "@devdigest/shared";
// Zod-free subpath: the barrel would pull zod + every schema into /settings.
import { FEATURE_MODELS } from "@devdigest/shared/constants/feature-models";
import { SectionTitle } from "../SectionTitle";
import { FeatureModelRow } from "./FeatureModelRow";
import { s } from "./styles";

/**
 * Settings → Feature Models. One provider + model picker per system LLM feature; the model list (and, for
 * OpenRouter, the prices) comes LIVE from the chosen provider (useProviderModels), and the choice persists
 * to `settings.feature_models`. A feature without its own choice uses the workspace
 * `default_model` when set (FR10), else its registry default.
 */
export function SettingsModels() {
  const t = useTranslations("settings");
  const { data: settings } = useSettings();
  const update = useUpdateSettings();
  const { data: models } = useProviderModels("openrouter");

  const chosen = (settings?.feature_models ?? {}) as Partial<Record<FeatureModelId, FeatureModelChoice>>;
  const defaultModel = settings?.default_model ?? null;
  const noModels = models !== undefined && models.length === 0;

  const setChoice = (id: FeatureModelId, choice: FeatureModelChoice) =>
    update.mutate({ feature_models: { ...chosen, [id]: choice } });

  return (
    <div style={s.wrap}>
      <SectionTitle title={t("models.title")} body={t("models.body")} />

      {FEATURE_MODELS.map((f) => (
        <FeatureModelRow
          key={f.id}
          feature={f}
          own={chosen[f.id]}
          defaultModel={defaultModel}
          onPick={(choice) => setChoice(f.id, choice)}
        />
      ))}

      <div style={s.note}>
        <Icon.Info size={15} style={s.noteIcon} />
        <span>{noModels ? t("models.noKeyNote") : t("models.liveNote")}</span>
      </div>
    </div>
  );
}
