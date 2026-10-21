"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useAgents, useRunEstimates, useRunReview } from "@/lib/hooks";
import { defaultSelectedIds, estimatesByAgent } from "./helpers";

interface Options {
  prId: string;
  onRunStart?: () => void;
  onRunsStarted?: (runIds: string[]) => void;
  onRunSettled?: () => void;
}

/** State of the Run Review popover: open flag, agent selection (defaults to the enabled
 *  agents until the user edits it), estimates and the start action. */
export function useRunReviewPicker({ prId, onRunStart, onRunsStarted, onRunSettled }: Options) {
  const router = useRouter();
  const [open, setOpen] = React.useState(false);
  const [picked, setPicked] = React.useState<string[] | null>(null);
  const { data: agentsData } = useAgents();
  const { data: estimateList } = useRunEstimates({ enabled: open });
  const run = useRunReview();

  const agents = React.useMemo(() => agentsData ?? [], [agentsData]);
  const estimates = React.useMemo(() => estimatesByAgent(estimateList), [estimateList]);
  const selected = React.useMemo(() => new Set(picked ?? defaultSelectedIds(agents)), [picked, agents]);
  const selectedIds = agents.filter((a) => selected.has(a.id)).map((a) => a.id);

  const toggle = (id: string) =>
    setPicked(agents.filter((a) => (a.id === id ? !selected.has(id) : selected.has(a.id))).map((a) => a.id));

  const start = () => {
    if (selectedIds.length === 0) return;
    const multi = selectedIds.length >= 2;
    if (!multi) onRunStart?.();
    // Errors are toasted by the global MutationCache; the popover and selection stay as they are.
    run.mutate(
      { prId, agentIds: selectedIds },
      {
        onSuccess: (res) => {
          if (multi && res.multi_agent_run_id) {
            router.push(`/multi-agent/${res.multi_agent_run_id}`);
          } else {
            onRunsStarted?.(res.runs.map((r) => r.run_id));
          }
          setOpen(false);
        },
        onSettled: () => onRunSettled?.(),
      },
    );
  };

  return {
    open,
    setOpen,
    agents,
    estimates,
    selected,
    selectedIds,
    toggle,
    selectAll: () => setPicked(agents.map((a) => a.id)),
    clear: () => setPicked([]),
    start,
    isPending: run.isPending,
    configure: () => {
      setOpen(false);
      router.push(`/multi-agent/new?pr=${encodeURIComponent(prId)}`);
    },
    goToAgents: () => router.push("/agents"),
  };
}
