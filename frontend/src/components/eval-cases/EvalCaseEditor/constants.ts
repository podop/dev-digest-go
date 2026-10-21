import type { EvalExpectationKind, FindingCategory, Severity } from "@devdigest/shared";

/** The preview and the inline checks follow the last keystroke after this pause (FR10). */
export const DEBOUNCE_MS = 300;

export const KINDS: readonly EvalExpectationKind[] = ["must_find", "must_not_flag"];
export const SEVERITIES: readonly Severity[] = ["CRITICAL", "WARNING", "SUGGESTION"];
export const CATEGORIES: readonly FindingCategory[] = ["bug", "security", "perf", "style", "test"];

/** Contiguous line ranges listed in an inline message before "…". */
export const RANGES_SHOWN = 6;

/** A header-less hunk header at the start of a line (what the server parser needs). */
export const HUNK_HEADER_LINE = /^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@/m;

/** `diff --git` / `+++ ` at the start of a line: the paste carries its own file headers. */
export const FILE_HEADER_LINE = /^(diff --git|\+\+\+ )/m;
