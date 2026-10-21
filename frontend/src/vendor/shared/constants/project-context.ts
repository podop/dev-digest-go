/**
 * Zod-free Project Context limits and the default glob, re-exported by
 * contracts/project-context. Import this subpath
 * (`@devdigest/shared/constants/project-context`) from client bundles that must
 * not pull zod in.
 */

/** Doc types, in the order they are recognised in a path (last segment wins). */
export const PROJECT_CONTEXT_DOC_TYPES = ['specs', 'docs', 'insights'] as const;
export type ProjectContextDocType = (typeof PROJECT_CONTEXT_DOC_TYPES)[number];

/** Default repo-relative glob; the server can override it (PROJECT_CONTEXT_GLOBS). */
export const PROJECT_CONTEXT_DEFAULT_GLOBS: readonly string[] = ['**/{specs,docs,insights}/**/*.md'];

/** Estimated tokens one run may add from project context. */
export const PROJECT_CONTEXT_BUDGET_TOKENS = 16_000;
/** A document larger than this is `too_large` at run time and 413 on preview. */
export const PROJECT_CONTEXT_MAX_DOC_BYTES = 262_144;
/** The list endpoint returns at most this many documents (first by path). */
export const PROJECT_CONTEXT_MAX_DOCS = 500;
/** Attached paths per owner and repo. */
export const PROJECT_CONTEXT_MAX_PATHS = 50;
/** Longest accepted repo-relative path. */
export const PROJECT_CONTEXT_PATH_MAX = 512;

/** Folder of the DevDigest-owned store files (DB rows, not clone files). */
export const PROJECT_CONTEXT_STORE_ROOT = '.devdigest/specs/';
/** Store files per repo. */
export const PROJECT_CONTEXT_STORE_MAX_FILES = 500;
/** Folder levels allowed below the store root. */
export const PROJECT_CONTEXT_STORE_MAX_DEPTH = 5;
/** One path segment of a store path: letters, digits, `.`, `_`, `-` (`.` / `..` are rejected separately). */
export const PROJECT_CONTEXT_STORE_SEGMENT_RE = /^[A-Za-z0-9._-]+$/;
