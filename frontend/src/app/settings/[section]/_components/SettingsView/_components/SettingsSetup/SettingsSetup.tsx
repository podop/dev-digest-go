"use client";

import React from "react";
import { useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { SectionPane } from "@/components/settings-kit";
import { SetupView } from "@/app/setup/_components/SetupView";
import { setupHref } from "@/app/setup/_components/SetupView/constants";
import { SectionTitle } from "../SectionTitle";

/** The flow reads `?step=` itself (useSearchParams needs a Suspense boundary of its own). */
function SetupFlow() {
  const step = useSearchParams().get("step") ?? undefined;
  return <SetupView requestedStep={step} hrefForStep={setupHref} />;
}

/**
 * Settings → First-run setup: the model provider → code host → repository flow, inline in the
 * settings pane (no page frame). Steps live in `?step=` of /settings/setup; Finish opens the
 * repository's PR list. This is also where repositories are added (the repo switcher has no
 * "Add repository" item).
 */
export function SettingsSetup() {
  const t = useTranslations("settingsSetup");
  return (
    <SectionPane maxWidth={560}>
      <SectionTitle title={t("title")} body={t("body")} />
      <React.Suspense fallback={null}>
        <SetupFlow />
      </React.Suspense>
    </SectionPane>
  );
}
