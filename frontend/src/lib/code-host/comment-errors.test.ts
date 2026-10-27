import { describe, it, expect } from "vitest";
import { ApiError } from "../api";
import { messages } from "@/test/render";
import {
  commentErrorKey,
  commentErrorMessage,
  commentErrorReason,
  commentRetryAfterSeconds,
  isDiffReloadError,
  type CommentErrorKey,
  type CommentErrorTranslate,
} from "./comment-errors";

const err = (code: string, reason?: string, extra: Record<string, unknown> = {}, message = "server says so") =>
  new ApiError(message, code === "repo_host_not_connected" ? 422 : 400, code, reason ? { reason, cause: "", ...extra } : extra);

/** The real English copy, so a missing key fails here. */
const COPY = messages.prCommentErrors as unknown as Record<CommentErrorKey, string>;
const t: CommentErrorTranslate = (key, values) =>
  COPY[key].replace("{seconds}", values ? String(values.seconds) : "");

// Every (code, reason) the API can answer with (spec §7, FR5–FR10) and the copy key it maps to.
const TABLE: [ApiError, CommentErrorKey][] = [
  [err("gitlab_unavailable"), "noToken"],
  [err("repo_host_not_connected", undefined, { host: "gl.example", repo_id: "r1" }), "hostNotConnected"],
  [err("gitlab_permission_denied", "token_rejected"), "tokenRejected"],
  [err("gitlab_permission_denied", "read_only_token"), "readOnlyToken"],
  [err("gitlab_permission_denied", "forbidden"), "forbidden"],
  [err("gitlab_rate_limited", "rate_limited", { retry_after_s: 37 }), "rateLimited"],
  [err("gitlab_comment_failed", "body_too_long"), "bodyTooLong"],
  [err("gitlab_comment_failed", "file_not_in_diff"), "fileNotInDiff"],
  [err("gitlab_comment_failed", "line_not_in_diff"), "lineNotInDiff"],
  [err("gitlab_comment_failed", "diff_not_ready"), "diffNotReady"],
  [err("gitlab_comment_failed", "diff_changed"), "diffChanged"],
  [err("gitlab_comment_failed", "reply_target_not_found"), "replyTargetNotFound"],
  [err("gitlab_comment_failed", "not_found"), "notFound"],
  [err("gitlab_comment_failed", "position_rejected"), "positionRejected"],
  [err("gitlab_comment_failed", "rejected"), "rejected"],
  [err("gitlab_comment_failed", "redirect"), "redirect"],
  [err("gitlab_comment_failed", "unavailable"), "unavailable"],
];

describe("commentErrorKey / commentErrorMessage", () => {
  it.each(TABLE)("maps %j to its copy and shows the server message when there is one", (e, key) => {
    expect(commentErrorKey(e)).toBe(key);
    expect(commentErrorMessage(e, t)).toBe("server says so");
    const bare = new ApiError("", e.status, e.code, e.details);
    expect(commentErrorMessage(bare, t)).toBe(COPY[key].replace("{seconds}", key === "rateLimited" ? "37" : ""));
    expect(commentErrorMessage(bare)).toBeNull();
  });

  it("keeps a copy entry for every key the code can return", () => {
    for (const [, key] of TABLE) expect(COPY[key]).toBeTruthy();
    expect(COPY.failed).toBeTruthy();
  });

  it("falls back to the code when the reason is unknown or missing", () => {
    expect(commentErrorKey(err("gitlab_comment_failed", "something_new"))).toBe("failed");
    expect(commentErrorKey(err("gitlab_comment_failed"))).toBe("failed");
    expect(commentErrorKey(err("gitlab_permission_denied"))).toBe("forbidden");
  });

  it("uses 60 seconds when a 429 carries no usable retry_after_s", () => {
    expect(commentRetryAfterSeconds(err("gitlab_rate_limited", "rate_limited", { retry_after_s: 37 }))).toBe(37);
    expect(commentRetryAfterSeconds(err("gitlab_rate_limited", "rate_limited"))).toBe(60);
    expect(commentRetryAfterSeconds(err("gitlab_rate_limited", "rate_limited", { retry_after_s: 0 }))).toBe(60);
    expect(commentRetryAfterSeconds(err("gitlab_rate_limited", "rate_limited", { retry_after_s: "9" }))).toBe(60);
    const bare = new ApiError("", 400, "gitlab_rate_limited", { reason: "rate_limited" });
    expect(commentErrorMessage(bare, t)).toContain("Try again in 60 s.");
  });

  it("passes any other error's own message through and says nothing for a blank one", () => {
    expect(commentErrorMessage(new ApiError("Cannot reach the DevDigest engine", 0, "network_error"))).toBe(
      "Cannot reach the DevDigest engine",
    );
    expect(commentErrorMessage(new Error("boom"), t)).toBe("boom");
    expect(commentErrorMessage(new Error("  "), t)).toBeNull();
    expect(commentErrorMessage("nope", t)).toBeNull();
    expect(commentErrorKey(new ApiError("x", 500))).toBeNull();
    expect(commentErrorReason(new ApiError("x", 400, "gitlab_comment_failed", ["reason"]))).toBeNull();
  });
});

describe("isDiffReloadError", () => {
  it("is true for diff_changed and diff_not_ready, by reason or by code, and for nothing else", () => {
    expect(isDiffReloadError(err("gitlab_comment_failed", "diff_changed"))).toBe(true);
    expect(isDiffReloadError(err("gitlab_comment_failed", "diff_not_ready"))).toBe(true);
    expect(isDiffReloadError(new ApiError("m", 400, "diff_changed"))).toBe(true);
    expect(isDiffReloadError(err("gitlab_comment_failed", "position_rejected"))).toBe(false);
    expect(isDiffReloadError(err("gitlab_comment_failed", "line_not_in_diff"))).toBe(false);
    expect(isDiffReloadError(err("gitlab_unavailable"))).toBe(false);
    expect(isDiffReloadError(new Error("diff_changed"))).toBe(false);
  });
});
