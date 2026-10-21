import type { ContextDoc } from "@devdigest/shared";
import {
  PROJECT_CONTEXT_MAX_DOC_BYTES,
  PROJECT_CONTEXT_PATH_MAX,
  PROJECT_CONTEXT_STORE_MAX_DEPTH,
  PROJECT_CONTEXT_STORE_ROOT,
  PROJECT_CONTEXT_STORE_SEGMENT_RE,
} from "@devdigest/shared/constants/project-context";

type RawParam = string | string[] | undefined;

export type ContextMode = "preview" | "edit";

export interface ContextSearch {
  /** Repo-relative path of the selected document, from ?doc=. */
  doc: string | null;
  /** Right-pane mode, from ?mode=edit (anything else = preview). */
  mode: ContextMode;
}

const first = (v: RawParam): string | undefined => (Array.isArray(v) ? v[0] : v);

/** Normalises the page's raw searchParams. */
export function parseContextSearch(raw: { doc?: RawParam; mode?: RawParam }): ContextSearch {
  const doc = first(raw.doc);
  return { doc: doc ? doc : null, mode: first(raw.mode) === "edit" ? "edit" : "preview" };
}

/** Screen URL for a selection; no document → the bare route. Preview is the default mode and is omitted. */
export function contextHref(repoId: string, doc: string | null, mode: ContextMode = "preview"): string {
  const base = `/repos/${encodeURIComponent(repoId)}/context`;
  if (!doc) return base;
  const params = new URLSearchParams({ doc });
  if (mode === "edit") params.set("mode", "edit");
  return `${base}?${params.toString()}`;
}

/** True when a draft exists and differs from the saved content. */
export function isDirty(draft: string | null, saved: string): boolean {
  return draft !== null && draft !== saved;
}

/** File name and its folder (with a trailing `/`, empty at the repo root). */
export function splitPath(path: string): { name: string; folder: string } {
  const i = path.lastIndexOf("/");
  return i < 0 ? { name: path, folder: "" } : { name: path.slice(i + 1), folder: path.slice(0, i + 1) };
}

/** Documents whose path contains the query (case-insensitive); order is kept. */
export function filterDocs(docs: readonly ContextDoc[], query: string): ContextDoc[] {
  const q = query.trim().toLowerCase();
  return q ? docs.filter((d) => d.path.toLowerCase().includes(q)) : [...docs];
}

/** The store files (editable, DevDigest-owned) and the clone documents, each in list order. */
export function splitBySource(docs: readonly ContextDoc[]): { store: ContextDoc[]; repo: ContextDoc[] } {
  return {
    store: docs.filter((d) => d.source === "store"),
    repo: docs.filter((d) => d.source !== "store"),
  };
}

/** Path shown in the store section: the part below `.devdigest/specs/`. */
export function storeRelative(path: string): string {
  return path.startsWith(PROJECT_CONTEXT_STORE_ROOT) ? path.slice(PROJECT_CONTEXT_STORE_ROOT.length) : path;
}

/** Shape rule of a store path — mirrors the server's `checkStorePath` (the server stays the authority). */
export function checkStorePath(path: string): boolean {
  if (path.length > PROJECT_CONTEXT_PATH_MAX) return false;
  if (!path.startsWith(PROJECT_CONTEXT_STORE_ROOT) || !path.endsWith(".md")) return false;
  const segments = path.slice(PROJECT_CONTEXT_STORE_ROOT.length).split("/");
  if (segments.length - 1 > PROJECT_CONTEXT_STORE_MAX_DEPTH) return false;
  return segments.every((seg) => seg !== "." && seg !== ".." && PROJECT_CONTEXT_STORE_SEGMENT_RE.test(seg));
}

/** Full store path for what the user typed in the inline rename (relative to the store root); null when invalid. */
export function renameTarget(relative: string): string | null {
  const path = PROJECT_CONTEXT_STORE_ROOT + relative.trim();
  return checkStorePath(path) ? path : null;
}

/** `<folder>/untitled.md` path for the New folder dialog; null when the folder name is invalid. */
export function newFolderPath(name: string): string | null {
  const folder = name.trim().replace(/\/+$/, "");
  if (!folder) return null;
  const path = `${PROJECT_CONTEXT_STORE_ROOT}${folder}/untitled.md`;
  return checkStorePath(path) ? path : null;
}

export type UploadSkipReason = "not_md" | "bad_name" | "too_large" | "not_utf8" | "has_nul";

export type UploadVerdict = { ok: true; content: string } | { ok: false; reason: UploadSkipReason };

/** The part of a browser `File` that classification needs. */
export interface UploadFile {
  name: string;
  size: number;
  arrayBuffer: () => Promise<ArrayBuffer>;
}

/** Decides whether a picked file can be created as a store file: `.md`, a valid name, ≤ 256 KB,
 *  UTF-8 text without NUL. The size is checked before the file is read. */
export async function classifyUpload(file: UploadFile): Promise<UploadVerdict> {
  if (!file.name.toLowerCase().endsWith(".md")) return { ok: false, reason: "not_md" };
  if (!checkStorePath(PROJECT_CONTEXT_STORE_ROOT + file.name)) return { ok: false, reason: "bad_name" };
  if (file.size > PROJECT_CONTEXT_MAX_DOC_BYTES) return { ok: false, reason: "too_large" };
  const bytes = await file.arrayBuffer();
  if (bytes.byteLength > PROJECT_CONTEXT_MAX_DOC_BYTES) return { ok: false, reason: "too_large" };
  let content: string;
  try {
    content = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return { ok: false, reason: "not_utf8" };
  }
  if (content.includes("\0")) return { ok: false, reason: "has_nul" };
  return { ok: true, content };
}
