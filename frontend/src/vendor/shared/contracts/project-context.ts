import { z } from 'zod';
import {
  PROJECT_CONTEXT_BUDGET_TOKENS,
  PROJECT_CONTEXT_DEFAULT_GLOBS,
  PROJECT_CONTEXT_DOC_TYPES,
  PROJECT_CONTEXT_MAX_DOCS,
  PROJECT_CONTEXT_MAX_DOC_BYTES,
  PROJECT_CONTEXT_MAX_PATHS,
  PROJECT_CONTEXT_PATH_MAX,
  PROJECT_CONTEXT_STORE_MAX_DEPTH,
  PROJECT_CONTEXT_STORE_MAX_FILES,
  PROJECT_CONTEXT_STORE_ROOT,
  PROJECT_CONTEXT_STORE_SEGMENT_RE,
} from '../constants/project-context.js';

/**
 * Project Context — repo markdown docs (specs / docs / insights) attached per
 * repo to agents and skills, read at the PR base commit and injected into the
 * prompt as untrusted data. Limits live in constants/project-context (zod-free).
 */
export {
  PROJECT_CONTEXT_BUDGET_TOKENS,
  PROJECT_CONTEXT_DEFAULT_GLOBS,
  PROJECT_CONTEXT_DOC_TYPES,
  PROJECT_CONTEXT_MAX_DOCS,
  PROJECT_CONTEXT_MAX_DOC_BYTES,
  PROJECT_CONTEXT_MAX_PATHS,
  PROJECT_CONTEXT_PATH_MAX,
  PROJECT_CONTEXT_STORE_MAX_DEPTH,
  PROJECT_CONTEXT_STORE_MAX_FILES,
  PROJECT_CONTEXT_STORE_ROOT,
  PROJECT_CONTEXT_STORE_SEGMENT_RE,
};

export const ContextDocType = z.enum(PROJECT_CONTEXT_DOC_TYPES);
export type ContextDocType = z.infer<typeof ContextDocType>;

/** Where a document lives: the repo clone (read-only) or the DevDigest store (editable). */
export const ContextDocSource = z.enum(['repo', 'store']);
export type ContextDocSource = z.infer<typeof ContextDocSource>;

/** One listed document: a clone file (never stored) or a store file. */
export const ContextDoc = z.object({
  path: z.string(),
  name: z.string(),
  doc_type: ContextDocType,
  size_bytes: z.number().int(),
  /** ceil(chars / 4). */
  tokens: z.number().int(),
  updated_at: z.string(),
  /** Distinct agents using it in this repo (directly or via a linked skill). */
  used_by: z.number().int(),
  source: ContextDocSource,
  /** True only for `store` documents. */
  editable: z.boolean(),
  /** Store files only; starts at 1, +1 on every content save or rename. */
  version: z.number().int().optional(),
});
export type ContextDoc = z.infer<typeof ContextDoc>;

/** GET /repos/:id/context */
export const ContextList = z.object({
  clone_status: z.enum(['ready', 'not_cloned']),
  globs: z.array(z.string()),
  docs: z.array(ContextDoc),
  tokens_total: z.number().int(),
  /** Present (true) only when the list was cut at the document cap. */
  truncated: z.boolean().optional(),
});
export type ContextList = z.infer<typeof ContextList>;

export const ContextUsedByAgent = z.object({
  id: z.string(),
  name: z.string(),
  via: z.enum(['direct', 'skill']),
  skill_name: z.string().optional(),
});
export type ContextUsedByAgent = z.infer<typeof ContextUsedByAgent>;

/** GET /repos/:id/context/doc?path= */
export const ContextDocPreview = z.object({
  path: z.string(),
  name: z.string(),
  doc_type: ContextDocType,
  content: z.string(),
  tokens: z.number().int(),
  size_bytes: z.number().int(),
  used_by: z.number().int(),
  used_by_agents: z.array(ContextUsedByAgent),
  source: ContextDocSource,
  editable: z.boolean(),
  version: z.number().int().optional(),
});
export type ContextDocPreview = z.infer<typeof ContextDocPreview>;

/** GET/PUT /agents/:id/context and /skills/:id/context — ordered attached paths. */
export const ContextAttachments = z.object({
  repo_id: z.string(),
  paths: z.array(z.string()),
});
export type ContextAttachments = z.infer<typeof ContextAttachments>;

/**
 * PUT body. Deliberately loose: the domain owns `invalid_path`,
 * `duplicate_path` and `too_many_paths` (422), so a shape error here must not
 * pre-empt them.
 */
export const ContextAttachmentsInput = z.object({
  repo_id: z.string(),
  paths: z.array(z.string()),
});
export type ContextAttachmentsInput = z.infer<typeof ContextAttachmentsInput>;

/**
 * Store-file write bodies. Deliberately loose on `path`/`content`: the domain
 * owns `invalid_path` (422) and `doc_too_large` (413), so a shape error here
 * must not pre-empt them.
 */
/** POST /repos/:id/context/files — no `path` creates `.devdigest/specs/untitled.md` with `suffix`. */
export const ContextFileCreateInput = z.object({
  path: z.string().optional(),
  content: z.string().optional(),
  on_conflict: z.enum(['fail', 'suffix']).optional(),
});
export type ContextFileCreateInput = z.infer<typeof ContextFileCreateInput>;

/** PUT /repos/:id/context/files?path= */
export const ContextFileSaveInput = z.object({
  content: z.string(),
  base_version: z.number().int(),
});
export type ContextFileSaveInput = z.infer<typeof ContextFileSaveInput>;

/** POST /repos/:id/context/files/rename */
export const ContextFileRenameInput = z.object({
  path: z.string(),
  new_path: z.string(),
  base_version: z.number().int(),
});
export type ContextFileRenameInput = z.infer<typeof ContextFileRenameInput>;

export const ProjectContextDocStatus = z.enum([
  'included',
  'missing',
  'too_large',
  'over_budget',
  'unreadable',
]);
export type ProjectContextDocStatus = z.infer<typeof ProjectContextDocStatus>;

export const ProjectContextOrigin = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('agent') }),
  z.object({ kind: z.literal('skill'), skill_id: z.string(), skill_name: z.string() }),
]);
export type ProjectContextOrigin = z.infer<typeof ProjectContextOrigin>;

/** One document of a run's project context, in prompt order. */
export const ProjectContextTraceDoc = z.object({
  path: z.string(),
  doc_type: z.string(),
  origin: ProjectContextOrigin,
  tokens: z.number().int(),
  status: ProjectContextDocStatus,
  /** Absent on traces written before store files = `repo`. */
  source: ContextDocSource.optional(),
  /** The text as sent — only for `included` documents. */
  text: z.string().optional(),
});
export type ProjectContextTraceDoc = z.infer<typeof ProjectContextTraceDoc>;

/** RunTrace.project_context — absent on runs without attachments and on old traces. */
export const ProjectContextTrace = z.object({
  budget_tokens: z.number().int(),
  tokens_total: z.number().int(),
  docs: z.array(ProjectContextTraceDoc),
});
export type ProjectContextTrace = z.infer<typeof ProjectContextTrace>;
