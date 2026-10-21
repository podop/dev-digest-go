import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { MultiAgentRunView } from "./_components/MultiAgentRunView";
import { resolveTraceRunId, resolveView } from "./helpers";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("runs");
  return { title: t("multiAgent.results.title") };
}

type Props = {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ view?: string | string[]; trace?: string | string[] }>;
};

/* Route: /multi-agent/:id — results of one multi-agent run (specs/multi-agent-review.md FR16–FR21).
   ?view=columns|tabs (default columns) and ?trace=<runId> (opens that agent run's trace drawer).
   Thin entry: the screen lives in _components/MultiAgentRunView. */
export default async function MultiAgentResultsPage({ params, searchParams }: Props) {
  const [{ id }, { view, trace }] = await Promise.all([params, searchParams]);
  return <MultiAgentRunView id={id} view={resolveView(view)} traceRunId={resolveTraceRunId(trace)} />;
}
