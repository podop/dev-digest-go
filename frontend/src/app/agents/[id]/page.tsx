import type { Metadata } from "next";
import { resolveStatsPeriod } from "@/lib/stats-period";
import { AgentEditorView, resolveTab } from "./_components/AgentEditorView";

export const metadata: Metadata = { title: "Agent" };

type Param = string | string[] | undefined;
type Props = {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ tab?: Param; period?: Param; from?: Param; to?: Param }>;
};

/* Route: /agents/:id — Agent Editor. Thin entry: resolves the id, ?tab= and the Stats tab's period
   (?period=1d|30d|custom, custom adds ?from=&to=; invalid → 30d), the screen lives in _components/AgentEditorView. */
export default async function AgentEditorPage({ params, searchParams }: Props) {
  const [{ id }, { tab, period, from, to }] = await Promise.all([params, searchParams]);
  return <AgentEditorView id={id} tab={resolveTab(tab)} period={resolveStatsPeriod({ period, from, to })} />;
}
