/* BlastRadiusCard helpers — the stat row (counting what the server returned),
   the client-built GitHub deep-link of a caller, how a caller is labelled and the message key of a
   degraded reason. Types only from @devdigest/shared (client/INSIGHTS.md). */
import type { BlastCaller, BlastDegradedReason, BlastRadius } from "@devdigest/shared";
import { githubBlobUrl } from "@/lib/github-urls";

export interface BlastStats {
  symbols: number;
  callers: number;
  endpoints: number;
  crons: number;
}

/** Counts of the response, distinct like the server's `summary` (a caller reaching two symbols counts once). */
export function blastStats(blast: BlastRadius): BlastStats {
  const callers = new Set<string>();
  const endpoints = new Set<string>();
  const crons = new Set<string>();
  for (const d of blast.downstream) {
    for (const c of d.callers) callers.add(`${c.file}\u0000${c.name}\u0000${c.line}`);
    for (const e of d.endpoints_affected) endpoints.add(e);
    for (const c of d.crons_affected) crons.add(c);
  }
  return { symbols: blast.changed_symbols.length, callers: callers.size, endpoints: endpoints.size, crons: crons.size };
}

function baseName(file: string): string {
  return file.slice(file.lastIndexOf("/") + 1);
}

/** The facade falls back to the caller file's name when it finds no enclosing function — that is no symbol, so no `()`. */
export function isFileName(caller: BlastCaller): boolean {
  return caller.name === caller.file || caller.name === baseName(caller.file);
}

/** `name()` for a real symbol; null when the "name" is only the caller's file name (show file:line alone). */
export function callerSymbolLabel(caller: BlastCaller): string | null {
  return isFileName(caller) ? null : `${caller.name}()`;
}

/** Drawing label of a caller: `name()`, or the file's basename when there is no symbol. */
export function callerNodeLabel(caller: BlastCaller): string {
  return callerSymbolLabel(caller) ?? baseName(caller.file);
}

/** The caller's exact line on GitHub at the PR head; undefined without a repo name or sha (render plain text). */
export function callerHref(repoFullName: string | null, headSha: string | null, caller: BlastCaller): string | undefined {
  if (!repoFullName || !headSha) return undefined;
  return githubBlobUrl(repoFullName, headSha, caller.file, caller.line);
}

const KNOWN_REASONS: readonly BlastDegradedReason[] = ["flag_off", "index_failed", "index_partial", "repo_too_large", "no_data"];

/** Key under `blast.degraded.reason.*`; a reason this client does not know falls back to `unknown`. */
export function degradedReasonKey(reason: string | undefined): string {
  return reason && (KNOWN_REASONS as readonly string[]).includes(reason) ? reason : "unknown";
}
