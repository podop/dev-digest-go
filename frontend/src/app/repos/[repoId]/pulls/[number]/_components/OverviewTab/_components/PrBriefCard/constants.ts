import type { Verdict } from "@devdigest/shared";

/** request_changes > comment > approve. */
export const VERDICT_RANK: Record<Verdict, number> = { approve: 0, comment: 1, request_changes: 2 };

/** Height of the loading skeleton (GET and POST). */
export const SKELETON_HEIGHT = 180;
