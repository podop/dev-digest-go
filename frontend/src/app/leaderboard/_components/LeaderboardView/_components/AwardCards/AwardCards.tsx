/* AwardCards — the six award cards of the leaderboard (FR20): Cleanest PR, Hottest PR, Fastest Merge,
   Most Rounds, Monster PR, Priciest Review. Each winner links to its PR detail page through its own
   `repo_id`. Presentational. */
"use client";

import type { LeaderboardAwards } from "@devdigest/shared";
import type { RepoLookup } from "../../helpers";
import { AwardCard } from "./AwardCard";
import { AWARD_SPECS } from "./helpers";
import { s } from "./styles";

export interface AwardCardsProps {
  awards: LeaderboardAwards;
  /** The response's repositories by id (GitLab references and host labels). */
  repos?: RepoLookup;
}

export function AwardCards({ awards, repos }: AwardCardsProps) {
  return (
    <div style={s.grid}>
      {AWARD_SPECS.map((spec) => {
        return <AwardCard key={spec.key} spec={spec} award={awards[spec.key]} repos={repos} />;
      })}
    </div>
  );
}
