"use client";

import { useTranslations } from "next-intl";
import { SectionPane } from "@/components/settings-kit";
import { SectionTitle } from "../SectionTitle";
import { GithubActionCard } from "./_components/GithubActionCard";
import { InstalledPlugins } from "./_components/InstalledPlugins";
import { PluginExportCard } from "./_components/PluginExportCard";
import { PluginImportCard } from "./_components/PluginImportCard";

/** Settings → Integrations: the GitHub Action card (FR2), plugin export (FR3) and import (FR6),
    with the installed plugins inside the import card as in the prototype (FR10). The cards own their queries, mutations
    and dialogs; this section only composes them (A10). */
export function SettingsIntegrations() {
  const t = useTranslations("settingsIntegrations");
  return (
    <SectionPane maxWidth={660}>
      <SectionTitle title={t("title")} body={t("body")} />
      <GithubActionCard />
      <PluginExportCard />
      <PluginImportCard>
        <InstalledPlugins />
      </PluginImportCard>
    </SectionPane>
  );
}
