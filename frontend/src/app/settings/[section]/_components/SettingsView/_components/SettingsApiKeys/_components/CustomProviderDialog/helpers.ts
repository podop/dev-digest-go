/* Pure form logic of the custom provider dialog: draft state, auth defaults (FR4), the request body (FR5:
   write-only secrets), validation before Save, and the 422 → field mapping. No React. */
import type {
  CustomProvider,
  CustomProviderAuthMode,
  CustomProviderHeaderInput,
  CustomProviderInput,
  CustomProviderProtocol,
  CustomProviderStructuredOutput,
  CustomProviderUpdate,
} from "@/lib/types";
import { slugFromName } from "@/lib/providers/custom";
import {
  draftsFromModels,
  hasModelProblems,
  modelsToInput,
  nextKey,
  type ModelDraft,
} from "../ManualModels";

export const MAX_HEADERS = 5;
export const NAME_MAX = 80;
export const BASE_URL_MAX = 2048;
/** Default auth header of an Anthropic-compatible gateway (FR4). */
export const DEFAULT_AUTH_HEADER = "x-api-key";

const SLUG = /^[a-z0-9-]{1,32}$/;

/** One extra-header row. `storedName` is the name the stored value belongs to (null for a new row). */
export interface HeaderDraft {
  key: string;
  name: string;
  value: string;
  storedName: string | null;
}

export interface FormState {
  name: string;
  slug: string;
  /** The user edited the slug, so it no longer follows the name. */
  slugTouched: boolean;
  protocol: CustomProviderProtocol;
  baseUrl: string;
  authMode: CustomProviderAuthMode;
  authHeader: string;
  /** The user picked the auth mode or header, so a protocol change leaves them alone. */
  authTouched: boolean;
  apiKey: string;
  /** Edit only: send `api_key: null`. */
  removeKey: boolean;
  structured: CustomProviderStructuredOutput;
  headers: HeaderDraft[];
  models: ModelDraft[];
}

export function authDefaults(protocol: CustomProviderProtocol): Pick<FormState, "authMode" | "authHeader"> {
  return protocol === "anthropic"
    ? { authMode: "header", authHeader: DEFAULT_AUTH_HEADER }
    : { authMode: "bearer", authHeader: "" };
}

export function emptyForm(): FormState {
  return {
    name: "",
    slug: "",
    slugTouched: false,
    protocol: "openai",
    baseUrl: "",
    ...authDefaults("openai"),
    authTouched: false,
    apiKey: "",
    removeKey: false,
    structured: "strict",
    headers: [],
    models: [],
  };
}

export function formFromProvider(p: CustomProvider): FormState {
  return {
    name: p.name,
    slug: p.id.slice("custom:".length),
    slugTouched: true,
    protocol: p.protocol,
    baseUrl: p.base_url,
    authMode: p.auth_mode,
    authHeader: p.auth_header ?? "",
    authTouched: true,
    apiKey: "",
    removeKey: false,
    structured: p.structured_output,
    headers: p.extra_headers.map((h) => ({
      key: nextKey("header"),
      name: h.name,
      value: "",
      storedName: h.value_configured ? h.name : null,
    })),
    models: draftsFromModels(p.models),
  };
}

export function newHeader(): HeaderDraft {
  return { key: nextKey("header"), name: "", value: "", storedName: null };
}

/** The row still holds its stored value: untouched value box and the name has not changed (FR5 matches names case-insensitively). */
export function keepsStoredValue(h: HeaderDraft): boolean {
  return h.storedName !== null && h.value === "" && h.name.trim().toLowerCase() === h.storedName.toLowerCase();
}

/** Where the typed name lands in the slug: it follows the name until the user edits the slug. */
export function withName(f: FormState, name: string): FormState {
  return { ...f, name, slug: f.slugTouched ? f.slug : slugFromName(name) };
}

/**
 * Switch protocol. Auth follows the protocol default until the user chose one; `json_object` is OpenAI-only, so
 * leaving OpenAI while it is selected falls back to Strict — `modeReset` tells the dialog to say so (FR34).
 */
export function withProtocol(f: FormState, protocol: CustomProviderProtocol): { form: FormState; modeReset: boolean } {
  if (protocol === f.protocol) return { form: f, modeReset: false };
  const modeReset = protocol === "anthropic" && f.structured === "json_object";
  return {
    form: {
      ...f,
      protocol,
      ...(f.authTouched ? {} : authDefaults(protocol)),
      structured: modeReset ? "strict" : f.structured,
    },
    modeReset,
  };
}

export function isValidSlug(slug: string): boolean {
  return SLUG.test(slug);
}

/** Why Save is off, by field; empty = the form can be sent (the server still has the last word). */
export function localProblems(f: FormState): Set<"name" | "slug" | "baseUrl" | "authHeader" | "headers" | "models"> {
  const out = new Set<"name" | "slug" | "baseUrl" | "authHeader" | "headers" | "models">();
  if (f.name.trim() === "") out.add("name");
  if (!isValidSlug(f.slug)) out.add("slug");
  if (f.baseUrl.trim() === "") out.add("baseUrl");
  if (f.authMode === "header" && f.authHeader.trim() === "") out.add("authHeader");
  if (f.headers.some((h) => h.name.trim() === "")) out.add("headers");
  if (hasModelProblems(f.models)) out.add("models");
  return out;
}

/**
 * The request body. Secrets are write-only (FR5): `api_key` is sent only when typed (or `null` to remove, edit
 * only); a header value only when typed, so an untouched stored header sends its name alone.
 */
export function buildUpdate(f: FormState, editing: boolean): CustomProviderUpdate {
  const key = f.apiKey.trim();
  const headers: CustomProviderHeaderInput[] = f.headers.map((h) => {
    const name = h.name.trim();
    return h.value === "" ? { name } : { name, value: h.value };
  });
  return {
    name: f.name.trim(),
    protocol: f.protocol,
    base_url: f.baseUrl.trim(),
    auth_mode: f.authMode,
    auth_header: f.authMode === "header" ? f.authHeader.trim() : null,
    structured_output: f.structured,
    ...(key !== "" ? { api_key: key } : editing && f.removeKey ? { api_key: null } : {}),
    extra_headers: headers,
    models: modelsToInput(f.models),
  };
}

export function buildCreate(f: FormState): CustomProviderInput {
  return { id: `custom:${f.slug}`, ...buildUpdate(f, false) };
}

/** A 422 `validation_error` split into messages by field path ("base_url", "extra_headers.0.value") and the rest. */
export interface ApiFieldErrors {
  fields: Record<string, string>;
  rest: string[];
}

const KNOWN_ROOTS = new Set([
  "id",
  "name",
  "protocol",
  "base_url",
  "auth_mode",
  "auth_header",
  "structured_output",
  "api_key",
  "extra_headers",
  "models",
]);

type PathPart = string | number;

function pathParts(path: unknown): PathPart[] | null {
  return Array.isArray(path) ? path.filter((p): p is PathPart => typeof p === "string" || typeof p === "number") : null;
}

/** Where an issue sits: `params.issue.path`, else `instancePath` ("/extra_headers/0/value"), else a top-level `path`. */
function issuePath(issue: Record<string, unknown>): PathPart[] {
  const params = issue.params as { issue?: { path?: unknown } } | null | undefined;
  const fromParams = pathParts(params?.issue?.path);
  if (fromParams) return fromParams;
  if (typeof issue.instancePath === "string") return issue.instancePath.split("/").filter((part) => part !== "");
  return pathParts(issue.path) ?? [];
}

function issueMessage(issue: Record<string, unknown>): string {
  if (typeof issue.message === "string" && issue.message !== "") return issue.message;
  const params = issue.params as { issue?: { message?: unknown } } | null | undefined;
  const nested = params?.issue?.message;
  return typeof nested === "string" ? nested : "";
}

/**
 * The 422 `details` of the server (`[{keyword, instancePath, schemaPath, message, params:{issue:{code, path, message}}}]`;
 * a plain `{path, message}` item also works). An issue without a known first path segment goes to `rest`.
 */
export function fieldErrorsFrom(details: unknown): ApiFieldErrors {
  const out: ApiFieldErrors = { fields: {}, rest: [] };
  if (!Array.isArray(details)) return out;
  for (const item of details) {
    if (typeof item !== "object" || item === null) continue;
    const issue = item as Record<string, unknown>;
    const message = issueMessage(issue);
    if (message === "") continue;
    const parts = issuePath(issue);
    const root = parts[0];
    if (typeof root !== "string" || !KNOWN_ROOTS.has(root)) {
      out.rest.push(message);
      continue;
    }
    out.fields[parts.join(".")] ??= message;
  }
  return out;
}
