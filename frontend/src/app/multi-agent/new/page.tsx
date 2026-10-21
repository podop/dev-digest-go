import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { ConfigureRunView } from "./_components/ConfigureRunView";
import { resolveAgentIds, resolvePrId } from "./helpers";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("runs");
  return { title: t("multiAgent.configure.title") };
}

type Props = { searchParams: Promise<{ pr?: string | string[]; agents?: string | string[] }> };

/* Route: /multi-agent/new — Configure run (specs/multi-agent-review.md FR15). Optional ?pr=<prId>
   preselects the PR, optional ?agents=<id>,<id> the agents (default: every enabled agent).
   Thin entry: the screen lives in _components/ConfigureRunView. */
export default async function ConfigureRunPage({ searchParams }: Props) {
  const { pr, agents } = await searchParams;
  return <ConfigureRunView prId={resolvePrId(pr)} agentIds={resolveAgentIds(agents)} />;
}
