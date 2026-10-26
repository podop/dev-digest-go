/* Pure logic of Settings → GitLab Integration: which connections the section tests on open (D28), the connection
   form draft and its request body (FR35: the token is write-only), the "Create a token" link and the 422 → field
   mapping. No React. */
import type { GitLabConnection, GitLabConnectionInput, GitLabConnectionUpdate } from "@/lib/types";

/** On opening the section the UI tests this many connections with a saved token, one at a time (FR33, D28). */
export const AUTO_TEST_LIMIT = 5;
export const DEFAULT_BASE_URL = "https://gitlab.com";
/** Mirror GITLAB_CONNECTION_LIMITS (the vendored contract) without pulling zod into the route. */
export const NAME_MAX = 80;
export const BASE_URL_MAX = 2048;

/** The first `AUTO_TEST_LIMIT` connections that have a token, in list order. */
export function autoTestIds(list: readonly GitLabConnection[]): string[] {
  return list
    .filter((c) => c.token_configured)
    .slice(0, AUTO_TEST_LIMIT)
    .map((c) => c.id);
}

/** The scope list as shown to the user ("api · read_repository"). */
export function scopesText(scopes: readonly string[]): string {
  return scopes.join(" · ");
}

function parseHttps(raw: string): URL | null {
  try {
    const url = new URL(raw.trim());
    return url.protocol === "https:" && url.hostname !== "" ? url : null;
  } catch {
    return null;
  }
}

/** The host (with a non-default port) of an https base URL: the placeholder of the name field (FR35); null if it is not one. */
export function hostOfBaseUrl(raw: string): string | null {
  return parseHttps(raw)?.host ?? null;
}

/** FR35 (D30): `<base_url>/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository`; null unless the base URL is https. */
export function createTokenUrl(raw: string): string | null {
  const url = parseHttps(raw);
  if (!url) return null;
  const root = `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
  return `${root}/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository`;
}

export interface FormState {
  name: string;
  baseUrl: string;
  /** Typed token; empty = leave as is. */
  token: string;
  /** Edit only: send `token: null`. */
  removeToken: boolean;
}

export function emptyForm(): FormState {
  return { name: "", baseUrl: DEFAULT_BASE_URL, token: "", removeToken: false };
}

/** The draft of a stored connection. The token is never read back: it stays empty. */
export function formFromConnection(c: GitLabConnection): FormState {
  return { name: c.name, baseUrl: c.base_url, token: "", removeToken: false };
}

/** Save is off while the base URL is empty or the name is too long; the server has the last word on the rest. */
export function canSubmit(f: FormState): boolean {
  return f.baseUrl.trim() !== "" && f.name.trim().length <= NAME_MAX;
}

export function buildCreate(f: FormState): GitLabConnectionInput {
  const name = f.name.trim();
  const token = f.token.trim();
  return { ...(name !== "" ? { name } : {}), base_url: f.baseUrl.trim(), ...(token !== "" ? { token } : {}) };
}

/** PUT body: the name always (empty = the host key); `base_url` only when it changed; a typed token replaces, "Remove token" sends null. */
export function buildUpdate(f: FormState, current: GitLabConnection): GitLabConnectionUpdate {
  const token = f.token.trim();
  const baseUrl = f.baseUrl.trim();
  return {
    name: f.name.trim(),
    ...(baseUrl !== current.base_url ? { base_url: baseUrl } : {}),
    ...(token !== "" ? { token } : f.removeToken ? { token: null } : {}),
  };
}

/** A 422 `validation_error` split into one message per field and the rest. */
export interface ApiFieldErrors {
  fields: Record<string, string>;
  rest: string[];
}

const KNOWN_FIELDS = new Set(["name", "base_url", "token"]);

type PathPart = string | number;

function pathParts(path: unknown): PathPart[] | null {
  return Array.isArray(path) ? path.filter((p): p is PathPart => typeof p === "string" || typeof p === "number") : null;
}

/** Where an issue sits: `params.issue.path`, else `instancePath` ("/base_url"), else a top-level `path` (frontend INSIGHTS: the server is not Zod-shaped). */
function issuePath(issue: Record<string, unknown>): PathPart[] {
  const params = issue.params as { issue?: { path?: unknown } } | null | undefined;
  const fromParams = pathParts(params?.issue?.path);
  if (fromParams) return fromParams;
  if (typeof issue.instancePath === "string") return issue.instancePath.split("/").filter((part) => part !== "");
  return pathParts(issue.path) ?? [];
}

function issueMessage(issue: Record<string, unknown>): string {
  if (typeof issue.message === "string" && issue.message !== "") return issue.message;
  const nested = (issue.params as { issue?: { message?: unknown } } | null | undefined)?.issue?.message;
  return typeof nested === "string" ? nested : "";
}

/** The 422 `details` of the server → messages by field (`name`, `base_url`, `token`); anything else goes to `rest`. */
export function fieldErrorsFrom(details: unknown): ApiFieldErrors {
  const out: ApiFieldErrors = { fields: {}, rest: [] };
  if (!Array.isArray(details)) return out;
  for (const item of details) {
    if (typeof item !== "object" || item === null) continue;
    const issue = item as Record<string, unknown>;
    const message = issueMessage(issue);
    if (message === "") continue;
    const root = issuePath(issue)[0];
    if (typeof root !== "string" || !KNOWN_FIELDS.has(root)) {
      out.rest.push(message);
      continue;
    }
    out.fields[root] ??= message;
  }
  return out;
}
