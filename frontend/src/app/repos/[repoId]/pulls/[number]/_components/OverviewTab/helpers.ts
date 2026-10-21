export interface FileRef {
  file: string;
  start_line: number;
  end_line: number | null;
}

/** `path:start` / `path:start-end` → parts; null for anything else (never navigated). */
export function parseFileRef(ref: string): FileRef | null {
  const m = /^(.+):(\d+)(?:-(\d+))?$/.exec(ref);
  if (!m) return null;
  const start = Number(m[2]);
  if (start < 1) return null;
  return { file: m[1] as string, start_line: start, end_line: m[3] ? Number(m[3]) : null };
}
