import { ApiError } from "@/lib/api";
import { pluginUploadKind, pluginUploadMaxBytes, type PluginUploadKind } from "@devdigest/shared/constants/plugins";

const MIB = 1024 * 1024;

/** How many `details.issues` of a 422 the card lists (the server sends up to 20). */
export const MAX_SHOWN_ISSUES = 5;

/** Conflicts of a preview or of a 409 `plugin_conflict` (`PluginConflictDetails`). */
export interface ImportConflicts {
  plugin: boolean;
  skills: string[];
}

/** A refused import, as the card shows it. */
export type ImportProblem =
  | { kind: "type" }
  | { kind: "size"; maxMb: number }
  /** 4xx with the server's message and the first issues of a 422 `invalid_plugin`. */
  | { kind: "message"; message: string; issues: string[] }
  /** 409 `plugin_conflict`: the conflicts replace the preview's. */
  | { kind: "conflict"; message: string; conflicts: ImportConflicts }
  /** Network failure, 5xx or anything unexpected: nothing was installed. */
  | { kind: "failed" };

export type FileCheck = { ok: true; kind: PluginUploadKind } | { ok: false; problem: ImportProblem };

/** Type and size check before any request (EC14); the server still enforces both. */
export function checkFile(file: Pick<File, "name" | "size">): FileCheck {
  const kind = pluginUploadKind(file.name);
  if (kind === null) return { ok: false, problem: { kind: "type" } };
  const max = pluginUploadMaxBytes(kind);
  if (file.size > max) return { ok: false, problem: { kind: "size", maxMb: Math.round(max / MIB) } };
  return { ok: true, kind };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
}

/** Reads `details` of a 409 `plugin_conflict`; null when it does not have the contract's shape. */
export function conflictsFromDetails(details: unknown): ImportConflicts | null {
  if (!isRecord(details) || typeof details.plugin !== "boolean") return null;
  return { plugin: details.plugin, skills: strings(details.skills) };
}

/** Maps a failed preview or install to what the card shows. */
export function classifyFailure(err: unknown): ImportProblem {
  // Network failures (status 0), 5xx and non-API errors: one generic line, nothing installed.
  if (!(err instanceof ApiError) || err.status < 400 || err.status >= 500) return { kind: "failed" };
  if (err.status === 409) {
    const conflicts = conflictsFromDetails(err.details);
    if (conflicts) return { kind: "conflict", message: err.message, conflicts };
  }
  const issues = isRecord(err.details) ? strings(err.details.issues).slice(0, MAX_SHOWN_ISSUES) : [];
  return { kind: "message", message: err.message, issues };
}

/** True when the conflicts block an install. */
export function hasConflicts(conflicts: ImportConflicts): boolean {
  return conflicts.plugin || conflicts.skills.length > 0;
}
