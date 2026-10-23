/* AwardCards — the six award cards of the leaderboard (FR20): Cleanest PR, Hottest PR, Fastest Merge,
   Most Rounds, Monster PR, Priciest Review. `repos` (the response's repository list) resolves the
   repository id the PR detail link needs. Presentational. */
"use client";

import React from "react";
import type { LeaderboardAwards, LeaderboardRepo } from "@devdigest/shared";
import { AwardCard } from "./AwardCard";
import { AWARD_SPECS, awardHref } from "./helpers";
import { s } from "./styles";

export interface AwardCardsProps {
  awards: LeaderboardAwards;
  repos: readonly LeaderboardRepo[];
}

export function AwardCards({ awards, repos }: AwardCardsProps) {
  return (
    <div style={s.grid}>
      {AWARD_SPECS.map((spec) => {
        const award = awards[spec.key];
        return <AwardCard key={spec.key} spec={spec} award={award} href={award ? awardHref(repos, award.repo, award.number) : null} />;
      })}
    </div>
  );
}
