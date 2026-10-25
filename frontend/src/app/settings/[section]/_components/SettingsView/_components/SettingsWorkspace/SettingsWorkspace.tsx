"use client";

import { useTranslations } from "next-intl";
import { SectionTitle } from "../SectionTitle";
import { DangerZone } from "./_components/DangerZone";
import { DataDirField } from "./_components/DataDirField";
import { RetentionField } from "./_components/RetentionField";
import { WorkspaceNameField } from "./_components/WorkspaceNameField";
import { WorkspaceStats } from "./_components/WorkspaceStats";
import { s } from "./styles";

/** Settings → Workspace: name (FR4), data directory (FR4), retention, counts and the danger zone.
    Each part loads and fails on its own, so a failing stats request leaves the fields usable (AC12). */
export function SettingsWorkspace() {
  const t = useTranslations("settingsWorkspace");
  return (
    <div style={s.wrap}>
      <SectionTitle title={t("title")} body={t("body")} />
      <WorkspaceNameField />
      <DataDirField />
      <RetentionField />
      <WorkspaceStats />
      <DangerZone />
    </div>
  );
}
