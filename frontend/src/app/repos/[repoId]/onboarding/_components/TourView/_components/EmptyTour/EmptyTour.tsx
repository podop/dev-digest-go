/* EmptyTour — "No tour yet": what Generate does (one LLM call, with the configured model) and the
   Generate button, disabled with a hint while the repo has no indexed files. */
"use client";

import { useTranslations } from "next-intl";
import { Button, EmptyState } from "@devdigest/ui";
import { useSettings } from "@/lib/hooks";
import { onboardingModel } from "./helpers";
import { s } from "./styles";

export function EmptyTour({
  indexNotReady,
  noSourceFiles,
  onGenerate,
}: {
  indexNotReady: boolean;
  noSourceFiles: boolean;
  onGenerate: () => void;
}) {
  const t = useTranslations("onboarding");
  const tc = useTranslations("common");
  const { data: settings } = useSettings();
  const model = settings ? onboardingModel(settings.feature_models) : null;
  return (
    <div style={s.root}>
      <EmptyState
        icon="Boxes"
        title={t("generate.title")}
        body={model ? t("generate.body", { model }) : t("generate.bodyUnknownModel")}
      />
      {noSourceFiles ? (
        <>
          <p style={s.hint}>{t("generate.noSourceFiles")}</p>
          <p style={s.hint}>{tc("repoIntel.supportedLanguages")}</p>
        </>
      ) : (
        indexNotReady && <p style={s.hint}>{t("generate.indexNotReady")}</p>
      )}
      <Button kind="primary" icon="Play" disabled={indexNotReady} onClick={onGenerate}>
        {t("generate.cta")}
      </Button>
    </div>
  );
}
