import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { MultiAgentListView } from "./_components/MultiAgentListView";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("runs");
  return { title: t("multiAgent.list.title") };
}

/* Route: /multi-agent — recent multi-agent runs of the active repo (specs/multi-agent-review.md FR14).
   Thin entry: the screen lives in _components/MultiAgentListView. */
export default function MultiAgentPage() {
  return <MultiAgentListView />;
}
