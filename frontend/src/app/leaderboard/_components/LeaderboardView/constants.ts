import type { LeaderboardPeriodKey } from "@devdigest/shared";

/** The period a missing or unknown ?period= falls back to (FR18, EC13). */
export const DEFAULT_PERIOD: LeaderboardPeriodKey = "week";

/** The repo filter value meaning "every repository"; also the fallback for a bad ?repo= (EC13, EC14). */
export const ALL_REPOS = "all";

/** Repository ids are UUIDs; any other ?repo= text cannot name a repository and is dropped without a request. */
export const REPO_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
