/* test/context-fixtures.ts — contract-shaped Project Context documents, lists
   and previews for the Context tabs and the shared picker. */
import type { ContextDoc, ContextDocPreview, ContextList } from "@devdigest/shared";
import type { Repo } from "@/lib/types";

export function makeDoc(path: string, overrides: Partial<ContextDoc> = {}): ContextDoc {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const type = path.startsWith("specs/") ? "specs" : path.startsWith("insights/") ? "insights" : "docs";
  return {
    path,
    name,
    doc_type: type,
    size_bytes: 400,
    tokens: 100,
    updated_at: "2026-10-01T00:00:00.000Z",
    used_by: 0,
    source: "repo",
    editable: false,
    ...overrides,
  };
}

/** 7 documents: 3 specs (100/200/300 tokens), 2 docs (400/500), 2 insights (600/700). */
export const DOC_PATHS = [
  "specs/security-baseline.md",
  "specs/public-api.md",
  "specs/rate-limiting.md",
  "docs/architecture.md",
  "docs/deployment.md",
  "insights/incident.md",
  "insights/perf-budget.md",
] as const;

export function makeList(overrides: Partial<ContextList> = {}): ContextList {
  const docs = DOC_PATHS.map((p, i) => makeDoc(p, { tokens: (i + 1) * 100 }));
  return {
    clone_status: "ready",
    globs: ["**/{specs,docs,insights}/**/*.md"],
    docs,
    tokens_total: docs.reduce((n, d) => n + d.tokens, 0),
    ...overrides,
  };
}

export function makePreview(path: string, content: string): ContextDocPreview {
  const doc = makeDoc(path);
  return {
    path,
    name: doc.name,
    doc_type: doc.doc_type,
    content,
    tokens: doc.tokens,
    size_bytes: doc.size_bytes,
    used_by: 0,
    used_by_agents: [],
    source: doc.source,
    editable: doc.editable,
  };
}

export function makeRepo(id: string, name: string): Repo {
  return {
    id,
    workspace_id: "w1",
    owner: "acme",
    name,
    full_name: `acme/${name}`,
    default_branch: "main",
    clone_path: `/clones/acme/${name}`,
    last_polled_at: null,
    created_by: null,
  };
}
