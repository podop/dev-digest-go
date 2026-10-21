import type { IconName } from "@devdigest/ui";
import type { BlastStats } from "./helpers";

export const VIEWS = ["tree", "graph"] as const;

export const STAT_KEYS = ["symbols", "callers", "endpoints", "crons"] as const satisfies readonly (keyof BlastStats)[];

export const STAT_ICON: Record<(typeof STAT_KEYS)[number], IconName> = {
  symbols: "Code",
  callers: "CornerDownRight",
  endpoints: "Globe",
  crons: "Clock",
};
