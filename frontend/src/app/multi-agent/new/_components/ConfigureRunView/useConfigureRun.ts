"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useAgents, usePulls, useRunEstimates, useRunReview } from "@/lib/hooks";
import { useActiveRepo } from "@/lib/repo-context";
import { MIN_AGENTS } from "./constants";
import { defaultSelectedIds, estimatesByAgent, footerTotals } from "./helpers";

interface Options {
  /** `?pr=` — preselected PR; ignored when it is not one of the active repo's PRs. */
  prId: string | null;
  /** `?agents=` — preselected agents; null → every enabled agent. */
  agentIds: string[] | null;
}

/** State of the Configure run screen: PR choice, agent selection (defaults until the user
 *  edits it), estimates with the parallel fan-out totals, and the start action. */
export function useConfigureRun({ prId, agentIds }: Options) {
  const router = useRouter();
  const { repoId, reposLoaded } = useActiveRepo();
  const pulls = usePulls(repoId);
  const agentsQuery = useAgents();
  const estimateQuery = useRunEstimates();
  const run = useRunReview();
  const [pickedPr, setPickedPr] = React.useState<string | null>(null);
  const [pickedAgents, setPickedAgents] = React.useState<string[] | null>(null);

  const pullList = React.useMemo(() => pulls.data ?? [], [pulls.data]);
  const agents = React.useMemo(() => agentsQuery.data ?? [], [agentsQuery.data]);
  const estimates = React.useMemo(() => estimatesByAgent(estimateQuery.data), [estimateQuery.data]);

  const knownPrId = prId && pullList.some((p) => p.id === prId) ? prId : null;
  const selectedPr = pickedPr ?? knownPrId ?? "";
  const selected = React.useMemo(
    () => new Set(pickedAgents ?? defaultSelectedIds(agents, agentIds)),
    [pickedAgents, agents, agentIds],
  );
  const selectedIds = agents.filter((a) => selected.has(a.id)).map((a) => a.id);
  const totals = footerTotals(selectedIds.map((id) => estimates.get(id)));

  const toggle = (id: string) =>
    setPickedAgents(agents.filter((a) => (a.id === id ? !selected.has(id) : selected.has(a.id))).map((a) => a.id));

  const start = () => {
    if (!selectedPr || selectedIds.length < MIN_AGENTS) return;
    // Errors are toasted by the global MutationCache; the selection stays as it is.
    run.mutate(
      { prId: selectedPr, agentIds: selectedIds },
      {
        onSuccess: (res) => {
          if (res.multi_agent_run_id) router.push(`/multi-agent/${encodeURIComponent(res.multi_agent_run_id)}`);
        },
      },
    );
  };

  return {
    hasRepo: !!repoId,
    reposLoaded,
    pulls: pullList,
    pullsLoading: pulls.isLoading,
    pullsError: pulls.isError,
    refetchPulls: pulls.refetch,
    agents,
    agentsLoading: agentsQuery.isLoading,
    agentsError: agentsQuery.isError,
    refetchAgents: agentsQuery.refetch,
    estimates,
    totals,
    selectedPr,
    setSelectedPr: setPickedPr,
    selected,
    selectedIds,
    toggle,
    selectAll: () => setPickedAgents(agents.map((a) => a.id)),
    clear: () => setPickedAgents([]),
    canStart: !!selectedPr && selectedIds.length >= MIN_AGENTS && !run.isPending,
    isStarting: run.isPending,
    start,
  };
}
