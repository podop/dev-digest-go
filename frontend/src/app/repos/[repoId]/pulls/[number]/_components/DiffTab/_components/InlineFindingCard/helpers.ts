import type { FindingRecord, PrCommentInput, PrReviewComment } from "@devdigest/shared";

/**
 * Hidden tag at the end of a posted comment. GitHub (and our <Markdown>) don't
 * render HTML comments, and the closing ` -->` keeps `f1` from matching `f10`,
 * so the live comment list tells which findings were already posted.
 */
export function findingMarker(findingId: string): string {
  return `<!-- devdigest-finding:${findingId} -->`;
}

/** The GitHub comment already posted for this finding, if any. */
export function findPublishedComment(
  comments: readonly PrReviewComment[] | undefined,
  findingId: string,
): PrReviewComment | undefined {
  const marker = findingMarker(findingId);
  return comments?.find((c) => c.body.includes(marker));
}

/** Prose outside code spans: raw HTML (links, images, an unclosed `<!--` that
    would swallow our signature and marker) becomes text, and a zero-width space
    after `@` keeps GitHub from turning it into a mention. */
function escapeProse(text: string): string {
  return text.replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll("@", "@​");
}

/**
 * LLM-written finding text (a PR author can steer it) made safe to post under
 * the reviewer's name. Code spans and fences — a backtick run up to the next
 * run of the same length — are kept verbatim: GitHub renders neither HTML nor
 * mentions inside them. Linear: a run length with no closer is remembered.
 */
export function sanitizeForGitHub(text: string): string {
  return mapProse(text, escapeProse);
}

/** `text` with `fn` applied to every stretch outside code spans and fences; code stays verbatim. */
function mapProse(text: string, fn: (prose: string) => string): string {
  let out = "";
  let from = 0;
  const unclosed = new Set<number>();
  for (;;) {
    const open = text.indexOf("`", from);
    if (open < 0) break;
    let end = open;
    while (text[end] === "`") end++;
    const run = end - open;
    const close = unclosed.has(run) ? -1 : closingRun(text, run, end);
    if (close < 0) unclosed.add(run);
    const codeEnd = close < 0 ? end : close + run;
    out += fn(text.slice(from, open)) + text.slice(open, codeEnd);
    from = codeEnd;
  }
  return out + fn(text.slice(from));
}

/** Start of the next backtick run of exactly `run` backticks at or after `from`, or -1. */
function closingRun(text: string, run: number, from: number): number {
  const fence = "`".repeat(run);
  for (let at = text.indexOf(fence, from); at >= 0; at = text.indexOf(fence, from)) {
    let end = at;
    while (text[end] === "`") end++;
    if (end - at === run) return at;
    from = end;
  }
  return -1;
}

/** U+200B, written as an escape so the source never holds the invisible character. */
const ZWSP = "\u200b";

/** Characters a GitLab quick action may follow at the start of a line (spec FR15). */
const QUICK_ACTION_SKIP = new Set([" ", "\t", "\uFEFF", "\u00A0", ">"]);
const isBlank = (c: string | undefined): boolean => c === " " || c === "\t";
const isDigit = (c: string | undefined): boolean => c != null && c >= "0" && c <= "9";

/** Length of the Markdown list marker at `at` (`-`, `*`, `+` or 1–9 digits then `.` or `)`, then a blank), else 0. */
function listMarkerLength(line: string, at: number): number {
  const c = line[at];
  if (c === "-" || c === "*" || c === "+") return isBlank(line[at + 1]) ? 1 : 0;
  let end = at;
  while (isDigit(line[end])) end++;
  const digits = end - at;
  return digits >= 1 && digits <= 9 && (line[end] === "." || line[end] === ")") && isBlank(line[end + 1])
    ? digits + 1
    : 0;
}

/** One line with U+200B put before a `/` that GitLab would run as a quick action. */
function neutraliseQuickAction(line: string): string {
  let i = 0;
  for (;;) {
    const c = line[i];
    if (c !== undefined && QUICK_ACTION_SKIP.has(c)) {
      i++;
      continue;
    }
    const marker = listMarkerLength(line, i);
    if (marker === 0) break;
    i += marker;
  }
  return line[i] === "/" ? `${line.slice(0, i)}${ZWSP}${line.slice(i)}` : line;
}

/**
 * GitLab FR15: a `/command` at the start of a line (after blanks, `>` and list markers) runs as a quick action when
 * the note is created, code spans included, so U+200B goes before the `/`. CR LF and a lone CR count as LF.
 */
function neutraliseQuickActions(text: string): string {
  return text.replace(/\r\n?/g, "\n").split("\n").map(neutraliseQuickAction).join("\n");
}

/** GitLab FR16(c), outside code spans only: `![` becomes `!` + U+200B + `[`, so no image is fetched. */
const escapeImages = (prose: string): string => prose.replaceAll("![", `!${ZWSP}[`);

/**
 * `sanitizeForGitHub` plus the GitLab-only rules of spec FR15 and FR16(c), for LLM text posted to a GitLab merge
 * request. The server applies the authoritative rules (FR13–FR17) to the whole body again; this only keeps the UI's
 * own text from relying on that.
 */
export function sanitizeForGitLab(text: string): string {
  return neutraliseQuickActions(mapProse(sanitizeForGitHub(text), escapeImages));
}

/** Which host's rules shape the body of a finding comment; GitHub unless `gitlab` is set. */
export interface CommentBodyOptions {
  gitlab?: boolean;
}

/** Markdown body of the review comment a finding is posted as. */
export function findingCommentBody(
  f: Pick<FindingRecord, "id" | "severity" | "category" | "title" | "rationale" | "suggestion" | "confidence">,
  { gitlab = false }: CommentBodyOptions = {},
): string {
  const sanitize = gitlab ? sanitizeForGitLab : sanitizeForGitHub;
  const parts = [`**[${f.severity} · ${f.category}] ${sanitize(f.title)}**`, sanitize(f.rationale.trim())];
  if (f.suggestion?.trim()) parts.push(`**Suggested fix**\n\n${sanitize(f.suggestion.trim())}`);
  parts.push(`<sub>DevDigest · ${Math.round(f.confidence * 100)}% confidence</sub>`, findingMarker(f.id));
  return parts.join("\n\n");
}

/** Inline comment on the finding's first line — the line its card sits under in the diff. */
export function findingCommentInput(f: FindingRecord, options: CommentBodyOptions = {}): PrCommentInput {
  return { path: f.file, line: f.start_line, side: "RIGHT", body: findingCommentBody(f, options) };
}
