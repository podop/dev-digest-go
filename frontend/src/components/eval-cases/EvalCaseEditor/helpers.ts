/* Pure rules of the case editor. The diff handling MIRRORS the server (FR2, FR3):
   `normalizeCaseDiff` = domain/case-input.ts, `parseCaseDiff` = adapters/git/diff-parser.ts,
   so a case the editor accepts passes the server's checks. */
import {
  EVAL_CASE_NAME_MAX,
  EVAL_EXPECTATION_FILE_MAX,
  EVAL_EXPECTATION_TITLE_MAX,
  EVAL_INPUT_DIFF_MAX_CHARS,
  EVAL_PR_DESCRIPTION_MAX,
  EVAL_PR_TITLE_MAX,
} from "@devdigest/shared/constants/evals";
import type {
  AgentEvalCase,
  AgentEvalCaseUpdateInput,
  EvalCaseCreateInput,
  EvalExpectationKind,
  FindingCategory,
  PrFile,
  Severity,
} from "@devdigest/shared";
import type { DiffHighlight } from "@/components/diff-viewer";
import { ApiError } from "@/lib/api";
import { CATEGORIES, FILE_HEADER_LINE, HUNK_HEADER_LINE, RANGES_SHOWN, SEVERITIES } from "./constants";

// ---- form values ----

/** Every field as the user sees it; line numbers stay strings while typing. */
export interface EditorValues {
  name: string;
  notes: string;
  kind: EvalExpectationKind;
  file: string;
  startLine: string;
  endLine: string;
  severity: Severity | "";
  category: FindingCategory | "";
  title: string;
  diff: string;
  prTitle: string;
  prDescription: string;
}

/** Touched fields only, laid over the stored case (or the empty form). */
export type EditorDraft = Partial<EditorValues>;

/** A stored severity / category in the contract's case ("critical" → "CRITICAL"), else none. */
function known<V extends string>(value: string | null, list: readonly V[], fold: (s: string) => string): V | "" {
  const hit = value == null ? undefined : list.find((v) => v === fold(value));
  return hit ?? "";
}

/** The form before any edit: the empty new-case form, or the stored case. */
export function baseValues(c: AgentEvalCase | null): EditorValues {
  if (!c) {
    return { name: "", notes: "", kind: "must_find", file: "", startLine: "", endLine: "", severity: "", category: "", title: "", diff: "", prTitle: "", prDescription: "" };
  }
  const e = c.expectation;
  return {
    name: c.name,
    notes: c.notes ?? "",
    kind: e.kind,
    file: e.file,
    startLine: String(e.start_line),
    endLine: String(e.end_line),
    severity: known(e.severity, SEVERITIES, (s) => s.toUpperCase()),
    category: known(e.category, CATEGORIES, (s) => s.toLowerCase()),
    title: e.title ?? "",
    diff: c.input_diff,
    prTitle: c.input_meta.pr_title,
    prDescription: c.input_meta.pr_description ?? "",
  };
}

export function isDirty(base: EditorValues, values: EditorValues): boolean {
  return (Object.keys(base) as (keyof EditorValues)[]).some((k) => base[k] !== values[k]);
}

// ---- the diff (mirrors the server) ----

/** A contiguous run of new-side line numbers of one file. */
export interface LineRange {
  start: number;
  end: number;
}

/** FR2: CRLF → LF; a paste with no `diff --git` / `+++ ` line becomes a single-file diff for `file`. */
export function normalizeCaseDiff(raw: string, file: string): string {
  const text = raw.replace(/\r\n/g, "\n");
  return FILE_HEADER_LINE.test(text) ? text : [`diff --git a/${file} b/${file}`, `--- a/${file}`, `+++ b/${file}`, text].join("\n");
}

interface ParsedFile {
  path: string;
  additions: number;
  deletions: number;
  /** Hunk lines, as `PrFile.patch`. */
  patch: string[];
  hunks: number;
  /** New-side numbers of added and context lines. */
  newLines: number[];
}

/** The server's `parseUnifiedDiff`, keeping the hunk text for the preview. Files without a path are dropped. */
function parseCaseDiff(raw: string): ParsedFile[] {
  const files: ParsedFile[] = [];
  let current: ParsedFile | null = null;
  let inHunk = false;
  let cursor = 0;
  const fresh = (): ParsedFile => ({ path: "", additions: 0, deletions: 0, patch: [], hunks: 0, newLines: [] });
  const flush = () => {
    if (current) files.push(current);
    current = null;
    inHunk = false;
  };

  for (const line of raw.split("\n")) {
    if (line.startsWith("diff --git")) {
      flush();
      current = fresh();
      continue;
    }
    if (line.startsWith("+++ ")) {
      current ??= fresh();
      const p = line.slice(4).replace(/^b\//, "").trim();
      current.path = p === "/dev/null" ? current.path : p;
      continue;
    }
    if (line.startsWith("--- ")) continue;
    const hh = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if (hh) {
      cursor = Number(hh[3]);
      inHunk = true;
      if (current) {
        current.hunks++;
        current.patch.push(line);
      }
      continue;
    }
    if (!current || !inHunk) continue;
    current.patch.push(line);
    if (line.startsWith("+") && !line.startsWith("+++")) {
      current.additions++;
      current.newLines.push(cursor++);
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      current.deletions++;
    } else {
      current.newLines.push(cursor++);
    }
  }
  flush();
  return files.filter((f) => f.path);
}

/** Sorted, merged runs of new-side lines (adjacent lines join one run). */
function toRanges(lines: number[]): LineRange[] {
  const ranges: LineRange[] = [];
  for (const n of [...new Set(lines)].sort((a, b) => a - b)) {
    const last = ranges[ranges.length - 1];
    if (last && n === last.end + 1) last.end = n;
    else ranges.push({ start: n, end: n });
  }
  return ranges;
}

/** What the editor knows about the pasted text. */
export interface DiffAnalysis {
  /** The paste carries its own `diff --git` / `+++ ` headers (the File field then offers its files). */
  hasHeaders: boolean;
  /** Some file has a hunk (the server's `diff_invalid` check). */
  hasHunk: boolean;
  /** Files for the preview: only those with a hunk. */
  files: PrFile[];
  /** Every path of the diff, in order (the File options). */
  paths: string[];
  /** New-side runs per path. */
  ranges: Record<string, LineRange[]>;
}

const EMPTY_ANALYSIS: DiffAnalysis = { hasHeaders: false, hasHunk: false, files: [], paths: [], ranges: {} };

/** Paste → preview files + the facts the checks need. A header-less paste needs a typed `file` to be parsed. */
export function splitPastedDiff(text: string, file: string): DiffAnalysis {
  if (text.trim() === "" || text.length > EVAL_INPUT_DIFF_MAX_CHARS) return EMPTY_ANALYSIS;
  const lf = text.replace(/\r\n/g, "\n");
  const hasHeaders = FILE_HEADER_LINE.test(lf);
  const path = file.trim();
  if (!hasHeaders && path === "") return { ...EMPTY_ANALYSIS, hasHunk: HUNK_HEADER_LINE.test(lf) };

  const parsed = parseCaseDiff(normalizeCaseDiff(lf, path));
  const ranges: Record<string, LineRange[]> = {};
  for (const f of parsed) ranges[f.path] = toRanges(parsed.filter((g) => g.path === f.path).flatMap((g) => g.newLines));
  return {
    hasHeaders,
    hasHunk: parsed.some((f) => f.hunks > 0),
    files: parsed
      .filter((f) => f.hunks > 0)
      .map((f) => ({ path: f.path, additions: f.additions, deletions: f.deletions, patch: trimTrailingBlank(f.patch).join("\n") })),
    paths: [...new Set(parsed.map((f) => f.path))],
    ranges,
  };
}

function trimTrailingBlank(lines: string[]): string[] {
  let end = lines.length;
  while (end > 0 && lines[end - 1] === "") end--;
  return lines.slice(0, end);
}

// ---- validation (FR11) ----

export interface CaseValidity {
  nameOk: boolean;
  /** Nothing pasted yet. */
  diffEmpty: boolean;
  diffTooLong: boolean;
  /** Text without a `@@` hunk header. */
  noHunk: boolean;
  fileBlank: boolean;
  /** Typed / selected file is not among the diff's files. */
  fileNotInDiff: boolean;
  /** Start or end is not a whole number ≥ 1 (blank counts as missing, not as invalid). */
  linesMissing: boolean;
  linesInvalid: boolean;
  rangeInverted: boolean;
  /** The file's changed or context lines, when the lines miss all of them. */
  linesOutside: LineRange[] | null;
  fieldTooLong: boolean;
  ok: boolean;
}

/** A positive whole line number, else null. */
export function parseLine(value: string): number | null {
  const n = Number(value);
  return value.trim() !== "" && Number.isInteger(n) && n >= 1 ? n : null;
}

export function validateCase(values: EditorValues, analysis: DiffAnalysis): CaseValidity {
  const name = values.name.trim();
  const file = values.file.trim();
  const start = parseLine(values.startLine);
  const end = parseLine(values.endLine);
  const diffEmpty = values.diff.trim() === "";
  const diffTooLong = values.diff.length > EVAL_INPUT_DIFF_MAX_CHARS;
  const noHunk = !diffEmpty && !diffTooLong && !analysis.hasHunk;
  const fileNotInDiff = file !== "" && analysis.hasHunk && analysis.hasHeaders && !analysis.paths.includes(file);
  const linesMissing = values.startLine.trim() === "" || values.endLine.trim() === "";
  const linesInvalid = (values.startLine.trim() !== "" && start == null) || (values.endLine.trim() !== "" && end == null);
  const rangeInverted = start != null && end != null && end < start;
  const runs = analysis.ranges[file];
  const linesOutside =
    start != null && end != null && !rangeInverted && analysis.hasHunk && file !== "" && !fileNotInDiff && runs && !runs.some((r) => r.start <= end && start <= r.end)
      ? runs
      : null;
  const fieldTooLong =
    file.length > EVAL_EXPECTATION_FILE_MAX ||
    values.title.length > EVAL_EXPECTATION_TITLE_MAX ||
    values.prTitle.length > EVAL_PR_TITLE_MAX ||
    values.prDescription.length > EVAL_PR_DESCRIPTION_MAX;
  const nameOk = name.length >= 1 && name.length <= EVAL_CASE_NAME_MAX;
  const ok =
    nameOk && !diffEmpty && !diffTooLong && !noHunk && file !== "" && !fileNotInDiff && !linesMissing && !linesInvalid && !rangeInverted && linesOutside == null && !fieldTooLong;
  return { nameOk, diffEmpty, diffTooLong, noHunk, fileBlank: file === "", fileNotInDiff, linesMissing, linesInvalid, rangeInverted, linesOutside, fieldTooLong, ok };
}

export type PreviewStatus = "empty" | "tooLong" | "noHunk" | "enterFile" | "ready";

/** What the preview shows: the diff, or the reason it cannot. */
export function previewStatus(validity: CaseValidity, analysis: DiffAnalysis): PreviewStatus {
  if (validity.diffEmpty) return "empty";
  if (validity.diffTooLong) return "tooLong";
  if (!analysis.hasHunk) return "noHunk";
  return analysis.files.length === 0 ? "enterFile" : "ready";
}

/** The range to light up in the preview: the typed file and lines, when they are usable. */
export function previewHighlight(values: EditorValues): DiffHighlight | null {
  const start = parseLine(values.startLine);
  const end = parseLine(values.endLine);
  const path = values.file.trim();
  return path !== "" && start != null && end != null && end >= start ? { path, start, end } : null;
}

/** "10–14, 20" — at most RANGES_SHOWN runs, then "…". */
export function formatRanges(ranges: readonly LineRange[]): string {
  const shown = ranges.slice(0, RANGES_SHOWN).map((r) => (r.start === r.end ? String(r.start) : `${r.start}–${r.end}`));
  return ranges.length > RANGES_SHOWN ? `${shown.join(", ")}, …` : shown.join(", ");
}

/** "12 345" for the counter ("N / 200 000"). */
export function formatCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

// ---- request bodies ----

const orNull = (text: string): string | null => (text.trim() === "" ? null : text);

/** POST body. Assumes `validateCase(...).ok`. The diff goes as pasted; the server wraps and checks it. */
export function buildCreateInput(values: EditorValues): EvalCaseCreateInput {
  return {
    name: values.name.trim(),
    notes: orNull(values.notes),
    expectation: {
      kind: values.kind,
      file: values.file.trim(),
      start_line: parseLine(values.startLine) ?? 1,
      end_line: parseLine(values.endLine) ?? 1,
      severity: values.severity || null,
      category: values.category || null,
      title: orNull(values.title.trim()),
    },
    input_diff: values.diff,
    input_meta: { pr_title: orNull(values.prTitle.trim()), pr_description: orNull(values.prDescription) },
  };
}

/** PATCH body with the changed fields only (both lines together when either moved); null when nothing changed. */
export function buildPatch(base: EditorValues, values: EditorValues): AgentEvalCaseUpdateInput | null {
  const patch: AgentEvalCaseUpdateInput = {};
  if (values.name.trim() !== base.name) patch.name = values.name.trim();
  if (values.notes !== base.notes) patch.notes = orNull(values.notes);
  if (values.kind !== base.kind) patch.kind = values.kind;
  if (values.file.trim() !== base.file) patch.file = values.file.trim();
  if (values.startLine !== base.startLine || values.endLine !== base.endLine) {
    const start = parseLine(values.startLine);
    const end = parseLine(values.endLine);
    if (start != null) patch.start_line = start;
    if (end != null) patch.end_line = end;
  }
  if (values.severity !== base.severity) patch.severity = values.severity || null;
  if (values.category !== base.category) patch.category = values.category || null;
  if (values.title !== base.title) patch.title = orNull(values.title.trim());
  if (values.diff !== base.diff) patch.input_diff = values.diff;
  if (values.prTitle !== base.prTitle) patch.pr_title = orNull(values.prTitle.trim());
  if (values.prDescription !== base.prDescription) patch.pr_description = orNull(values.prDescription);
  return Object.keys(patch).length ? patch : null;
}

// ---- server errors (FR11) ----

/** Where a rejected save is shown. */
export type ErrorField = "diff" | "file" | "lines" | "form";

export interface SaveError {
  field: ErrorField;
  /** Message key under `caseEditor.error.*`. */
  key: "diffInvalid" | "patchTooLarge" | "fileNotInDiff" | "linesOutside" | "limit" | "validation" | "notFound" | "generic";
  files?: string[];
  ranges?: LineRange[];
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
}

function rangeList(value: unknown): LineRange[] {
  if (!Array.isArray(value)) return [];
  const out: LineRange[] = [];
  for (const r of value) {
    const { start, end } = (r ?? {}) as { start?: unknown; end?: unknown };
    if (typeof start === "number" && typeof end === "number") out.push({ start, end });
  }
  return out;
}

/** Error code (and `details`) → the field to show it at. */
export function saveError(err: unknown): SaveError {
  if (err instanceof ApiError) {
    const details = (err.details ?? {}) as { files?: unknown; ranges?: unknown };
    switch (err.code) {
      case "diff_invalid":
        return { field: "diff", key: "diffInvalid" };
      case "patch_too_large":
        return { field: "diff", key: "patchTooLarge" };
      case "expectation_file_not_in_diff":
        return { field: "file", key: "fileNotInDiff", files: stringList(details.files) };
      case "expectation_lines_not_in_diff":
        return { field: "lines", key: "linesOutside", ranges: rangeList(details.ranges) };
      case "eval_case_limit":
        return { field: "form", key: "limit" };
      case "validation_error":
        return { field: "form", key: "validation" };
      case "not_found":
        return { field: "form", key: "notFound" };
    }
    if (err.status === 404) return { field: "form", key: "notFound" };
  }
  return { field: "form", key: "generic" };
}

export function formatDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
