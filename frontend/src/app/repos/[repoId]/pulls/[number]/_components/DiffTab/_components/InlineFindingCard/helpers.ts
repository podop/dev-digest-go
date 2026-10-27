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

/** Byte-for-byte mirror of the server's code regions (`notebody.codeRegions`, decision D-new-1): half-open `[start, end)`. */
interface Region {
  start: number;
  end: number;
}

/** Leading spaces of a line. */
function leadingSpaces(line: string): number {
  let n = 0;
  while (line[n] === " ") n++;
  return n;
}

/** A fence delimiter line: up to 3 spaces, then 3+ backticks or tildes. `rest` is the text after the run. */
function fenceLine(line: string): { ch: string; n: number; rest: string } | null {
  const indent = leadingSpaces(line);
  const ch = line[indent];
  if (indent > 3 || (ch !== "`" && ch !== "~")) return null;
  let end = indent;
  while (line[end] === ch) end++;
  return end - indent >= 3 ? { ch, n: end - indent, rest: line.slice(end) } : null;
}

/**
 * For every line, the index of the line closing a fence opened there, or -1. Right to left, one stack per fence
 * character that keeps (nearest last) only closers longer than every nearer one, so "the nearest closer at least
 * `n` long" is a binary search.
 */
function fenceClosers(lines: readonly string[]): number[] {
  const stacks: Record<string, { line: number; n: number }[]> = { "`": [], "~": [] };
  const closer = new Array<number>(lines.length).fill(-1);
  for (let i = lines.length - 1; i >= 0; i--) {
    const fence = fenceLine(lines[i] ?? "");
    const stack = fence ? stacks[fence.ch] : undefined;
    if (!fence || !stack) continue;
    if (fence.ch === "~" || !fence.rest.includes("`")) {
      let lo = 0; // first entry shorter than the opener; the entries before it are long enough
      let hi = stack.length;
      while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if ((stack[mid]?.n ?? 0) < fence.n) hi = mid;
        else lo = mid + 1;
      }
      if (lo > 0) closer[i] = stack[lo - 1]?.line ?? -1;
    }
    if (fence.rest.trim() === "") {
      while (stack.length > 0 && (stack[stack.length - 1]?.n ?? 0) <= fence.n) stack.pop();
      stack.push({ line: i, n: fence.n });
    }
  }
  return closer;
}

/** Whether a line opens a block quote or a list item (after blanks: `>`, or a list marker then a blank or the end). */
function startsContainer(line: string): boolean {
  const t = line.replace(/^[ \t]+/, "");
  return /^(?:>|[-*+](?:[ \t]|$)|[0-9]{1,9}[.)](?:[ \t]|$))/.test(t);
}

interface BacktickRun {
  start: number;
  end: number;
  openStart: number;
  openLen: number;
  lineEnd: number;
}

/** Inline spans: a backtick run and the next run of exactly the same length on the same line (escaped opener: one fewer). */
function inlineSpans(runs: readonly BacktickRun[]): Region[] {
  const pair = new Array<number>(runs.length).fill(-1);
  const nearest = new Map<number, number>();
  for (let i = runs.length - 1; i >= 0; i--) {
    const run = runs[i];
    if (!run) continue;
    if (run.openLen > 0) pair[i] = nearest.get(run.openLen) ?? -1;
    nearest.set(run.end - run.start, i);
  }
  const spans: Region[] = [];
  for (let i = 0; i < runs.length; ) {
    const run = runs[i];
    const j = pair[i] ?? -1;
    const closer = j >= 0 ? runs[j] : undefined;
    if (run && closer && closer.start < run.lineEnd) {
      spans.push({ start: run.openStart, end: closer.end });
      i = j + 1;
    } else {
      i++;
    }
  }
  return spans;
}

/**
 * Where GitLab surely renders code (decision D-new-1; over-neutralising a real span is fine): a CLOSED fenced block
 * (3+ backticks or tildes at column 0–3, closed by a fence of the same character, at least as long; a fence indented
 * 1–3 columns counts only while no list item or block quote can be open) and an inline span on ONE line (a backslash
 * escapes the opening backtick; a line with a pipe has none, a table row splits its cells there). Anything else,
 * indented code included, is prose. `text` has LF line ends.
 */
function gitlabCodeRegions(text: string): Region[] {
  const lines = text.split("\n");
  const closer = fenceClosers(lines);
  const regions: Region[] = [];
  const runs: BacktickRun[] = [];
  let containerSeen = false;
  let at = 0;
  const starts = lines.map((line) => {
    const start = at;
    at += line.length + 1;
    return start;
  });
  for (let i = 0; i < lines.length; ) {
    const line = lines[i] ?? "";
    const start = starts[i] ?? 0;
    const close = closer[i] ?? -1;
    if (close >= 0 && (leadingSpaces(line) === 0 || !containerSeen)) {
      regions.push({ start, end: (starts[close] ?? 0) + (lines[close]?.length ?? 0) });
      i = close + 1;
      continue;
    }
    if (!containerSeen && startsContainer(line)) containerSeen = true;
    if (!line.includes("|")) {
      for (const m of line.matchAll(/`+/g)) {
        const from = m.index ?? 0;
        let slashes = 0;
        while (from - 1 - slashes >= 0 && line[from - 1 - slashes] === "\\") slashes++;
        const escaped = slashes % 2 === 1 ? 1 : 0;
        runs.push({
          start: start + from,
          end: start + from + m[0].length,
          openStart: start + from + escaped,
          openLen: m[0].length - escaped,
          lineEnd: start + line.length,
        });
      }
    }
    i++;
  }
  return [...regions, ...inlineSpans(runs)].sort((a, b) => a.start - b.start);
}

/** A character reference: numeric (decimal or hex) or a named one of up to 8 letters, with its `;`. */
const CHARACTER_REFERENCE = /&(?:#[xX][0-9a-fA-F]+|#[0-9]+|[A-Za-z]{1,8});/g;
const SPECIAL_NAMES = new Set(["commat", "lt", "excl", "lsqb", "lbrack"]);
const SPECIAL_CODEPOINTS = new Set([0x40, 0x3c, 0x21, 0x5b]);

/**
 * Whether the character reference `ref` decodes to `@`, `<`, `!` or `[` (any letter case, any leading zeros). The
 * exact lower-case `&lt;` is not reported: it is what the rules write for `<` and decodes to inert text.
 */
function isSpecialReference(ref: string): boolean {
  if (ref === "&lt;") return false;
  if (ref[1] === "#") {
    const hex = ref[2] === "x" || ref[2] === "X";
    const value = Number.parseInt(ref.slice(hex ? 3 : 2, -1).replace(/^0+(?=.)/, ""), hex ? 16 : 10);
    return SPECIAL_CODEPOINTS.has(value);
  }
  return SPECIAL_NAMES.has(ref.slice(1, -1).toLowerCase());
}

/** Prose for GitLab: HTML and images become text, a mention gets a U+200B, an entity that decodes to one of them loses its `&`. */
function escapeGitLabProse(prose: string): string {
  return prose
    .replace(CHARACTER_REFERENCE, (ref) => (isSpecialReference(ref) ? `&amp;${ref.slice(1)}` : ref))
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replace(/@(?!​)/g, `@${ZWSP}`)
    .replaceAll("![", `!${ZWSP}[`);
}

/**
 * LLM text for a GitLab merge request, made safe to post under the reviewer's name: spec FR15 (quick actions, code
 * included) and FR16 outside code, with the server's conservative code regions (D-new-1) and entity rule (D-new-2).
 * The server applies the authoritative rules (FR13–FR17) to the whole body again; this only keeps the UI's own text
 * from relying on that. `sanitizeForGitHub` is unrelated and unchanged.
 */
export function sanitizeForGitLab(text: string): string {
  // Quick actions first: FR15 looks behind `>` prefixes, which the prose escape turns into `&gt;`.
  const lf = neutraliseQuickActions(text);
  let out = "";
  let from = 0;
  for (const region of gitlabCodeRegions(lf)) {
    out += escapeGitLabProse(lf.slice(from, region.start)) + lf.slice(region.start, region.end);
    from = region.end;
  }
  return out + escapeGitLabProse(lf.slice(from));
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
