import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { LeaderboardView, resolvePeriod, resolveRepoParam } from "./_components/LeaderboardView";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("leaderboard");
  return { title: t("metaTitle") };
}

type Param = string | string[] | undefined;
type Props = { searchParams: Promise<{ period?: Param; repo?: Param }> };

/* Route: /leaderboard — PR Leaderboard: ranked pull requests, KPIs and charts for a period
   (?period=week|last_week|30d) and a repository (?repo=all|<id>). Thin entry: normalizes the URL state
   (invalid values fall back to week / all), the screen lives in _components/LeaderboardView. */
export default async function LeaderboardPage({ searchParams }: Props) {
  const { period, repo } = await searchParams;
  return <LeaderboardView period={resolvePeriod(period)} repo={resolveRepoParam(repo)} />;
}
