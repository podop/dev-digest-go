import { ApiError } from "@/lib/api";
import type { FindingRecord } from "@devdigest/shared";

export type EvalCaseBlock = "no_agent" | "undecided";

/** Why "Turn into eval case" is unavailable (FR20); null when the finding qualifies. */
export function evalCaseBlock(f: Pick<FindingRecord, "accepted_at" | "dismissed_at">, agentId: string | null | undefined): EvalCaseBlock | null {
  if (!agentId) return "no_agent";
  if (!f.accepted_at && !f.dismissed_at) return "undecided";
  return null;
}

/** Server error codes of POST /findings/:id/eval-case that have their own message. */
const KNOWN_ERROR_CODES = [
  "not_found",
  "finding_undecided",
  "finding_no_agent",
  "patch_unavailable",
  "patch_too_large",
  "eval_case_limit",
] as const;

/** Message key (under `finding.evalCaseError`) for a failed create. */
export function evalCaseErrorKey(err: unknown): (typeof KNOWN_ERROR_CODES)[number] | "generic" {
  const code = err instanceof ApiError ? err.code : undefined;
  return KNOWN_ERROR_CODES.find((c) => c === code) ?? "generic";
}

/** Link target of "Eval case ✓" — the agent's Evals tab. */
export function evalsTabHref(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}?tab=evals`;
}
