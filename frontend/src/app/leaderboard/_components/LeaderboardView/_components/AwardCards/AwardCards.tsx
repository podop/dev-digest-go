/* AwardCards — the six award cards of the leaderboard (FR20): Cleanest PR, Hottest PR, Fastest Merge,
   Most Rounds, Monster PR, Priciest Review. Each winner links to its PR detail page through its own
   `repo_id`. Presentational. */
"use client";

import type { LeaderboardAwards } from "@devdigest/shared";
import { AwardCard } from "./AwardCard";
import { AWARD_SPECS } from "./helpers";
import { s } from "./styles";

export interface AwardCardsProps {
  awards: LeaderboardAwards;
}

export function AwardCards({ awards }: AwardCardsProps) {
  return (
    <div style={s.grid}>
      {AWARD_SPECS.map((spec) => {
        return <AwardCard key={spec.key} spec={spec} award={awards[spec.key]} />;
      })}
    </div>
  );
}
