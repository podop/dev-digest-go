import type { IconName } from "@devdigest/ui";
import type { SkillType } from "@devdigest/shared";

/** Skill types in display order (labels: skills.type.*). */
export const SKILL_TYPES: readonly SkillType[] = ["rubric", "convention", "security", "custom"];

/** Skill editor tabs reachable via ?tab= (labels: skills.editor.tabs.*). */
export const SKILL_TABS = ["config", "context", "preview", "versions", "stats", "evals"] as const;
export type SkillTab = (typeof SKILL_TABS)[number];

export const SKILL_TAB_ICONS: Record<SkillTab, IconName> = {
  config: "Settings",
  context: "FileText",
  preview: "Eye",
  versions: "History",
  stats: "BarChart",
  evals: "FlaskConical",
};

/** Tab shown when ?tab= is missing or unknown. */
export const DEFAULT_SKILL_TAB: SkillTab = "config";
