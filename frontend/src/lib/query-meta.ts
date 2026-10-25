/* query-meta.ts — typed `meta` for TanStack mutations.
   The global MutationCache (providers.tsx) toasts every mutation error. A
   mutation that shows some errors itself (inline in a modal, or a dedicated
   toast) lists their ApiError codes in `meta.quietErrorCodes`; "*" silences
   every error of that mutation — except a 402 `spend_cap_reached`, which is always
   toasted (mutation-errors.tsx, A6). */
import { ApiError } from "./api";

export interface MutationMeta extends Record<string, unknown> {
  /** ApiError codes the caller surfaces itself; "*" = all errors. */
  quietErrorCodes?: readonly string[];
}

declare module "@tanstack/react-query" {
  interface Register {
    mutationMeta: MutationMeta;
  }
}

/** Whether the mutation's own `meta` silences this error. The global toast still shows a
 *  402 spend-cap refusal regardless (see `reportMutationError`). */
export function isQuietError(err: unknown, meta: MutationMeta | undefined): boolean {
  const codes = meta?.quietErrorCodes;
  if (!codes?.length) return false;
  if (codes.includes("*")) return true;
  return err instanceof ApiError && err.code != null && codes.includes(err.code);
}
