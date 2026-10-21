/* pr-urls.ts — in-app deep-links into the PR detail screen. The PR list and the
   PR detail route both link a finding's file:line to the Files changed tab, so
   the URL format (?tab=diff&file=…&line=start[-end]) lives here, next to the
   parser the route reads it back with. */

/** Search params of a Files changed deep-link. */
export const DIFF_FILE_PARAM = "file";
export const DIFF_LINE_PARAM = "line";

/** A file + new-side line range to open, highlight and scroll to in the diff. */
export interface DiffFocus {
  path: string;
  start: number;
  end: number;
}

export function prDetailPath(repoId: string, number: string | number): string {
  return `/repos/${repoId}/pulls/${number}`;
}

/** /repos/:repoId/pulls/:number?tab=diff&file=<path>&line=12 (or 12-18). */
export function prDiffHref(
  repoId: string,
  number: string | number,
  f: { file: string; start_line: number; end_line?: number | null },
): string {
  const end = f.end_line != null && f.end_line > f.start_line ? f.end_line : null;
  const sp = new URLSearchParams({
    tab: "diff",
    [DIFF_FILE_PARAM]: f.file,
    [DIFF_LINE_PARAM]: end ? `${f.start_line}-${end}` : String(f.start_line),
  });
  return `${prDetailPath(repoId, number)}?${sp.toString()}`;
}

/** Read ?file/?line back; null when either is missing or the line isn't "N" / "N-M". */
export function parseDiffFocus(file: string | null, line: string | null): DiffFocus | null {
  if (!file || !line) return null;
  const m = /^(\d+)(?:-(\d+))?$/.exec(line);
  if (!m) return null;
  const start = Number(m[1]);
  const end = m[2] ? Number(m[2]) : start;
  if (start < 1) return null;
  return { path: file, start, end: Math.max(start, end) };
}
