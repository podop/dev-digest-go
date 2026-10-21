import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { EvalDashboardView } from "./_components/EvalDashboardView";
import { resolveAgentId, resolvePeriod } from "./helpers";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("eval");
  return { title: t("page.metaTitle") };
}

type Props = { searchParams: Promise<{ agent?: string | string[]; period?: string | string[] }> };

/* Route: /eval — Eval Dashboard: all agents, or one agent's view with ?agent=<id>&period=<7d|30d|90d|all>.
   Thin entry: resolves ?agent= and ?period= (unknown → 30d), the screen lives in _components/EvalDashboardView. */
export default async function EvalPage({ searchParams }: Props) {
  const { agent, period } = await searchParams;
  return <EvalDashboardView agentId={resolveAgentId(agent)} period={resolvePeriod(period)} />;
}
