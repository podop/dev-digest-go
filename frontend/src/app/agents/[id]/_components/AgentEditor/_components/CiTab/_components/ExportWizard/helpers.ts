import type { CiFile } from "@devdigest/shared";
import { ApiError } from "@/lib/api";
import { isCustomId } from "@/lib/providers/custom";
import {
  RUNNER_BUNDLE_PATH,
  TRIGGERS,
  WORKFLOW_PATH_PATTERN,
  type Runner,
  type Trigger,
  type TriggerState,
} from "./constants";

const KIB = 1024;
const MIB = KIB * KIB;

/** The ticked triggers in their fixed order (the request's `triggers`). */
export function triggersToList(state: TriggerState): Trigger[] {
  return TRIGGERS.filter((t) => state[t]);
}

/** The one editable file of the export: the workflow. */
export function findWorkflow(files: readonly CiFile[]): CiFile | undefined {
  return files.find((f) => f.editable);
}

/** The files as shown and shipped: the workflow carries the user's edit when there is one. */
export function withEditedWorkflow(files: readonly CiFile[], edited: string | null): CiFile[] {
  if (edited == null) return [...files];
  return files.map((f) => (f.editable ? { ...f, contents: edited } : f));
}

/** The agent slug the server chose, read back from the workflow's path; null until files exist. */
export function slugFromFiles(files: readonly CiFile[] | undefined): string | null {
  for (const f of files ?? []) {
    const m = WORKFLOW_PATH_PATTERN.exec(f.path);
    if (m?.[1]) return m[1];
  }
  return null;
}

export function isRunnerBundle(path: string): boolean {
  return path === RUNNER_BUNDLE_PATH;
}

export function byteSize(text: string): number {
  return new TextEncoder().encode(text).length;
}

/** "812 B" / "14.2 KB" / "1.3 MB". */
export function formatBytes(bytes: number): string {
  if (bytes < KIB) return `${bytes} B`;
  if (bytes < MIB) return `${(bytes / KIB).toFixed(1)} KB`;
  return `${(bytes / MIB).toFixed(1)} MB`;
}

export function zipFileName(slug: string): string {
  return `devdigest-ci-${slug}.zip`;
}

/** The link only when it is http(s): `pr_url` is GitHub data, never render other schemes. */
export function safeHttpUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const { protocol } = new URL(url);
    return protocol === "https:" || protocol === "http:" ? url : null;
  } catch {
    return null;
  }
}

export interface WizardError {
  code: string | null;
  message: string;
  /** `details.rule` of a `workflow_insecure` error. */
  rule: string | null;
}

/** An API error as the wizard shows it: its code, the server's message and the failed rule. */
export function describeError(err: unknown, fallback: string): WizardError {
  if (err instanceof ApiError) {
    const details = err.details;
    const rule =
      details && typeof details === "object" && "rule" in details && typeof details.rule === "string"
        ? details.rule
        : null;
    return { code: err.code ?? null, message: err.message || fallback, rule };
  }
  return { code: null, message: fallback, rule: null };
}

/** Self-hosted for a custom-provider agent (its gateway is usually private), GitHub-hosted otherwise (FR31, D18). */
export function defaultRunner(provider: string): Runner {
  return isCustomId(provider) ? "self_hosted" : "github_hosted";
}

/** What the wizard shows of the agent's custom provider. */
export interface CustomInfo {
  name: string;
  baseUrl: string;
  /** Host (with a non-default port) of the base URL. */
  host: string;
}

/** What the workflow needs in the repository settings, from the export response (FR31). */
export interface Requirements {
  secrets: readonly string[];
  variables: readonly string[];
  /** Set for a custom-provider agent: its base URL is the value of the variable. */
  custom: CustomInfo | null;
}

/** Whether a base URL points to the machine the studio runs on (`localhost`, 127.0.0.0/8, `::1`) (EC21). */
export function isLoopbackBaseUrl(baseUrl: string): boolean {
  let host: string;
  try {
    host = new URL(baseUrl).hostname.toLowerCase();
  } catch {
    return false;
  }
  return (
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "[::1]" ||
    /^127(\.\d{1,3}){3}$/.test(host)
  );
}
