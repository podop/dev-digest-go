"use client";

import { useTranslations } from "next-intl";
import { SectionTitle } from "../SectionTitle";
import { GithubActionCard } from "./_components/GithubActionCard";
import { InstalledPlugins } from "./_components/InstalledPlugins";
import { PluginExportCard } from "./_components/PluginExportCard";
import { PluginImportCard } from "./_components/PluginImportCard";
import { s } from "./styles";

/** Settings → Integrations: the GitHub Action card (FR2), plugin export (FR3) and import (FR6),
    then the installed plugins the import points to (FR10). The cards own their queries, mutations
    and dialogs; this section only composes them (A10). */
export function SettingsIntegrations() {
  const t = useTranslations("settingsIntegrations");
  return (
    <div style={s.wrap}>
      <SectionTitle title={t("title")} body={t("body")} />
      <div style={s.first}>
        <GithubActionCard />
      </div>
      <PluginExportCard />
      <PluginImportCard />
      <div style={s.last}>
        <InstalledPlugins />
      </div>
    </div>
  );
}
