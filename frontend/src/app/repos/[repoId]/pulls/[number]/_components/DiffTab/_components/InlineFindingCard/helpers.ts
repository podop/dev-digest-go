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
    out += escapeProse(text.slice(from, open)) + text.slice(open, codeEnd);
    from = codeEnd;
  }
  return out + escapeProse(text.slice(from));
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

/** Markdown body of the review comment a finding is posted as. */
export function findingCommentBody(
  f: Pick<FindingRecord, "id" | "severity" | "category" | "title" | "rationale" | "suggestion" | "confidence">,
): string {
  const parts = [
    `**[${f.severity} · ${f.category}] ${sanitizeForGitHub(f.title)}**`,
    sanitizeForGitHub(f.rationale.trim()),
  ];
  if (f.suggestion?.trim()) parts.push(`**Suggested fix**\n\n${sanitizeForGitHub(f.suggestion.trim())}`);
  parts.push(`<sub>DevDigest · ${Math.round(f.confidence * 100)}% confidence</sub>`, findingMarker(f.id));
  return parts.join("\n\n");
}

/** Inline comment on the finding's first line — the line its card sits under in the diff. */
export function findingCommentInput(f: FindingRecord): PrCommentInput {
  return { path: f.file, line: f.start_line, side: "RIGHT", body: findingCommentBody(f) };
}
