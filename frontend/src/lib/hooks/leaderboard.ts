/* hooks/leaderboard.ts — React Query hook of the PR Leaderboard page (specs/2026-10-23-pr-leaderboard.md
   §7). One read-only GET /leaderboard; period, repo and the browser's IANA zone are in the query key,
   so a switch is a new entry (no placeholder data: the old filter's numbers are never shown) and the
   latest key's response is the one rendered (EC12). Nothing polls. */
"use client";

import { useQuery } from "@tanstack/react-query";
import type { Leaderboard, LeaderboardPeriodKey } from "@devdigest/shared";
import { api } from "../api";
import { leaderboardKeys } from "./keys";

/** The viewer's IANA zone (e.g. "Europe/Kyiv"); "UTC" when the runtime cannot tell. */
export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export interface UseLeaderboardOptions {
  period: LeaderboardPeriodKey;
  /** "all" or a repository id. */
  repo: string;
  /** False holds the request back (e.g. until the repo id is checked against the repo list). */
  enabled?: boolean;
}

/** GET /leaderboard?period=&tz=&repo= — the whole page in one response. */
export function useLeaderboard({ period, repo, enabled = true }: UseLeaderboardOptions) {
  const tz = browserTimeZone();
  return useQuery({
    queryKey: leaderboardKeys.view(period, repo, tz),
    queryFn: () => {
      const search = new URLSearchParams({ period, tz, repo });
      return api.get<Leaderboard>(`/leaderboard?${search.toString()}`);
    },
    enabled,
  });
}
