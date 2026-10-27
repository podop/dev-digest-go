/* code-host/comment-errors.ts — errors of `POST /pulls/:id/comments` for a GitLab PR (GitLab B, spec FR5, FR7, FR10,
   FR20, §7). Pure: no React, no fetching. The server's message is authoritative and already names the connection,
   path and line; the copy keys here (`messages/en/prCommentErrors.json`) cover the case where the response carries
   none. Import as "@/lib/code-host/comment-errors". */
import { ApiError } from "../api";
import { GITLAB_ERROR_CODES } from "./codes";

/** 400 for every GitLab failure of a post that has no code of its own; `details.reason` tells which (FR10). */
export const GITLAB_COMMENT_FAILED_CODE = "gitlab_comment_failed";

/** `details.reason` values that mean the loaded diff is stale: the UI reloads the PR and its comments (FR20). */
export const DIFF_RELOAD_REASONS = ["diff_changed", "diff_not_ready"] as const;

/** Seconds shown when a 429 carries no usable `details.retry_after_s` (FR10: absent means 60). */
export const DEFAULT_RETRY_AFTER_S = 60;

/** Keys of the `prCommentErrors` message namespace. */
export type CommentErrorKey =
  | "tokenRejected"
  | "readOnlyToken"
  | "forbidden"
  | "notFound"
  | "positionRejected"
  | "rejected"
  | "rateLimited"
  | "redirect"
  | "unavailable"
  | "noToken"
  | "hostNotConnected"
  | "bodyTooLong"
  | "fileNotInDiff"
  | "lineNotInDiff"
  | "diffNotReady"
  | "diffChanged"
  | "replyTargetNotFound"
  | "failed";

/** `useTranslations("prCommentErrors")` fits this. */
export type CommentErrorTranslate = (key: CommentErrorKey, values?: { seconds: number }) => string;

/** `details.reason` → copy key (FR5–FR10). */
const KEY_BY_REASON: Readonly<Record<string, CommentErrorKey>> = {
  token_rejected: "tokenRejected",
  read_only_token: "readOnlyToken",
  forbidden: "forbidden",
  rate_limited: "rateLimited",
  body_too_long: "bodyTooLong",
  file_not_in_diff: "fileNotInDiff",
  line_not_in_diff: "lineNotInDiff",
  diff_not_ready: "diffNotReady",
  diff_changed: "diffChanged",
  reply_target_not_found: "replyTargetNotFound",
  not_found: "notFound",
  position_rejected: "positionRejected",
  rejected: "rejected",
  redirect: "redirect",
  unavailable: "unavailable",
};

/** Codes whose `details.reason` is absent or unknown: the code alone picks the copy. */
const KEY_BY_CODE: Readonly<Record<string, CommentErrorKey>> = {
  [GITLAB_ERROR_CODES.unavailable]: "noToken",
  [GITLAB_ERROR_CODES.hostNotConnected]: "hostNotConnected",
  [GITLAB_ERROR_CODES.permissionDenied]: "forbidden",
  [GITLAB_ERROR_CODES.rateLimited]: "rateLimited",
  [GITLAB_COMMENT_FAILED_CODE]: "failed",
};

function detailsOf(err: unknown): Record<string, unknown> | null {
  if (!(err instanceof ApiError)) return null;
  const d = err.details;
  return d != null && typeof d === "object" && !Array.isArray(d) ? (d as Record<string, unknown>) : null;
}

/** `details.reason` of a failed post, or null. */
export function commentErrorReason(err: unknown): string | null {
  const reason = detailsOf(err)?.reason;
  return typeof reason === "string" && reason !== "" ? reason : null;
}

/** `details.retry_after_s` of a 429 (a positive integer), else 60 (FR10). */
export function commentRetryAfterSeconds(err: unknown): number {
  const n = detailsOf(err)?.retry_after_s;
  return typeof n === "number" && Number.isInteger(n) && n > 0 ? n : DEFAULT_RETRY_AFTER_S;
}

/** Whether the failed post means the loaded diff is stale, so the PR detail and comments must reload (FR20, P12). */
export function isDiffReloadError(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  const reason = commentErrorReason(err);
  return [reason, err.code].some((v) => v != null && (DIFF_RELOAD_REASONS as readonly string[]).includes(v));
}

/** The copy key for a failed post: by `details.reason`, else by `code`; null for an error that is not a post error. */
export function commentErrorKey(err: unknown): CommentErrorKey | null {
  if (!(err instanceof ApiError) || err.code == null) return null;
  const reason = commentErrorReason(err);
  if (reason != null && KEY_BY_REASON[reason] != null) return KEY_BY_REASON[reason];
  return KEY_BY_CODE[err.code] ?? null;
}

/**
 * The text to toast for a failed post (FR20): the server's message when the response has a code and a message (it
 * names the connection, path and line); otherwise the localized copy for its reason or code; for any other
 * `Error` its message; null when there is nothing to say (the caller shows its own fallback).
 */
export function commentErrorMessage(err: unknown, t?: CommentErrorTranslate): string | null {
  if (err instanceof ApiError && commentErrorKey(err) != null) {
    if (err.message.trim() !== "") return err.message;
    const key = commentErrorKey(err);
    if (t && key) return t(key, key === "rateLimited" ? { seconds: commentRetryAfterSeconds(err) } : undefined);
    return null;
  }
  return err instanceof Error && err.message.trim() !== "" ? err.message : null;
}
