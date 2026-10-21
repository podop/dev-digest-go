import type { CiFile } from "@devdigest/shared";
import { ApiError } from "@/lib/api";
import { FOCUSABLE_SELECTOR, RUNNER_BUNDLE_PATH, TRIGGERS, WORKFLOW_PATH_PATTERN, type Trigger, type TriggerState } from "./constants";

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

/** The visible tab stops inside a dialog, in DOM order. */
export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (el) => !el.hasAttribute("disabled") && el.getAttribute("aria-hidden") !== "true",
  );
}

/** Where Tab should go to stay inside the dialog; null = let the browser move focus. */
export function wrapTarget(
  stops: readonly HTMLElement[],
  active: Element | null,
  shift: boolean,
  inside: boolean,
): HTMLElement | null {
  const first = stops[0];
  const last = stops[stops.length - 1];
  if (!first || !last) return null;
  if (!inside) return first;
  if (shift && active === first) return last;
  if (!shift && active === last) return first;
  return null;
}
