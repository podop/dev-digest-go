/* SettingsGithub — Settings → GitHub Integration (FR17–FR19): the token row with its test, the
   read-only comment identity and the connected repositories. The section lists no endpoints of
   its own: it reuses the secrets-status, test-connection and repo hooks. */
"use client";

import { useTranslations } from "next-intl";
import { SectionPane } from "@/components/settings-kit";
import { SectionTitle } from "../SectionTitle";
import { ConnectedRepos } from "./_components/ConnectedRepos";
import { TokenSection } from "./_components/TokenSection";

export function SettingsGithub() {
  const t = useTranslations("githubIntegration");
  return (
    <SectionPane>
      <SectionTitle title={t("title")} body={t("body")} />
      <TokenSection />
      <ConnectedRepos />
    </SectionPane>
  );
}
