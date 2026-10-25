/* mutation-errors.tsx — how the global MutationCache surfaces a failed mutation.
   Normally one error toast, unless the mutation silences it (`meta.quietErrorCodes`).
   Exception (A6): a 402 `spend_cap_reached` is ALWAYS toasted, with a link to the spend
   cap field in Settings → API Keys — even for mutations that silence every error with "*",
   because the user must learn why their LLM action did nothing. */
import { SPEND_CAP_ERROR_CODE } from "@devdigest/shared/constants/settings";
import { ApiError } from "./api";
import { isQuietError, type MutationMeta } from "./query-meta";
import { notify, type ToastAction } from "./toast";

export const SPEND_CAP_TOAST_ACTION: ToastAction = {
  href: "/settings/api-keys",
  label: "spendCapSettings",
};

export function isSpendCapError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 402 && err.code === SPEND_CAP_ERROR_CODE;
}

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return "Something went wrong";
}

/** MutationCache `onError`: toast the error unless the mutation surfaces it itself. */
export function reportMutationError(err: unknown, meta: MutationMeta | undefined): void {
  if (isSpendCapError(err)) {
    notify.error(errorMessage(err), SPEND_CAP_TOAST_ACTION);
    return;
  }
  if (!isQuietError(err, meta)) notify.error(errorMessage(err));
}
