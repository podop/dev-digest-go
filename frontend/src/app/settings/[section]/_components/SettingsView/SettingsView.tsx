/* Settings — left sub-nav (FR1 order) + the section pane, deep-linked at /settings/:section.
   The page validates the key; sections without a panel yet show the "coming later" empty state. */
"use client";

import type React from "react";
import Link from "next/link";
import { EmptyState } from "@devdigest/ui";
import { useTranslations } from "next-intl";
import { AppShell } from "@/components/app-shell";
import { SettingsAbout } from "./_components/SettingsAbout";
import { SettingsApiKeys } from "./_components/SettingsApiKeys";
import { SettingsAutoReviews } from "./_components/SettingsAutoReviews";
import { SettingsGithub } from "./_components/SettingsGithub";
import { SettingsGitlab } from "./_components/SettingsGitlab";
import { SettingsIntegrations } from "./_components/SettingsIntegrations";
import { SettingsModels } from "./_components/SettingsModels";
import { SettingsWorkspace } from "./_components/SettingsWorkspace";
import { DEFAULT_SECTION, SETTINGS_SECTION_KEYS, sectionHref, type SettingsSectionKey } from "./constants";
import { s } from "./styles";

/** Sections that have a panel; the others render the "coming in a later release" empty state. */
const PANELS: Partial<Record<SettingsSectionKey, React.ComponentType>> = {
  "api-keys": SettingsApiKeys,
  github: SettingsGithub,
  gitlab: SettingsGitlab,
  workspace: SettingsWorkspace,
  "automatic-reviews": SettingsAutoReviews,
  integrations: SettingsIntegrations,
  models: SettingsModels,
  about: SettingsAbout,
};

export function SettingsView({ section }: { section: SettingsSectionKey }) {
  const t = useTranslations("settingsShell");
  const Panel = PANELS[section];
  const label = t(`sections.${section}`);

  return (
    <AppShell crumb={[{ label: t("breadcrumb"), href: sectionHref(DEFAULT_SECTION) }, { label }]}>
      <div style={s.layout}>
        <nav style={s.nav} aria-label={t("navLabel")}>
          <h1 style={s.navTitle}>{t("title")}</h1>
          {SETTINGS_SECTION_KEYS.map((key) => {
            const on = key === section;
            return (
              <Link key={key} href={sectionHref(key)} aria-current={on ? "page" : undefined}>
                <div style={s.navItem(on)}>{t(`sections.${key}`)}</div>
              </Link>
            );
          })}
        </nav>
        <div style={s.pane}>
          {Panel ? <Panel /> : <EmptyState icon="Settings" title={label} body={t("comingSoon")} />}
        </div>
      </div>
    </AppShell>
  );
}
