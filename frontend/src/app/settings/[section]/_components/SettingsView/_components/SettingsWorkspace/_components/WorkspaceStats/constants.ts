import type { WorkspaceStats } from "@devdigest/shared";

/** Tiles in the FR12 order: message key under `workspaceStats.tiles` and the field of GET /workspace/stats. */
export const STAT_TILES = [
  { key: "agents", field: "agents" },
  { key: "skills", field: "skills" },
  { key: "evalCases", field: "eval_cases" },
  { key: "learnings", field: "learnings" },
  { key: "repos", field: "repos" },
  { key: "runs", field: "runs" },
] as const satisfies readonly { key: string; field: keyof WorkspaceStats }[];
