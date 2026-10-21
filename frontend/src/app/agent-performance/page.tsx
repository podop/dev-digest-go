import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { resolveStatsPeriod } from "@/lib/stats-period";
import { AgentPerformanceView, resolveSort } from "./_components/AgentPerformanceView";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("agentPerformance");
  return { title: t("dashboard.metaTitle") };
}

type Param = string | string[] | undefined;
type Props = { searchParams: Promise<{ period?: Param; from?: Param; to?: Param; sort?: Param; dir?: Param }> };

/* Route: /agent-performance — Agent Performance: one row per agent for a period (?period=1d|30d|custom,
   custom adds ?from=&to= as YYYY-MM-DD), sorted by ?sort=&dir=. Thin entry: resolves the URL state
   (invalid values fall back to 30d / accept rate desc), the screen lives in _components/AgentPerformanceView. */
export default async function AgentPerformancePage({ searchParams }: Props) {
  const { period, from, to, sort, dir } = await searchParams;
  return <AgentPerformanceView period={resolveStatsPeriod({ period, from, to })} sort={resolveSort(sort, dir)} />;
}
