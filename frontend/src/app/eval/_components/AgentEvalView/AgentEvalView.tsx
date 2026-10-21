/* AgentEvalView — /eval?agent=<id>&period=<p>: one agent's eval history (FR24): header with
   agent picker, period control and Run eval, the regression banner, latest-run tiles with
   deltas, the metric trend and the runs table with selection of exactly two finished runs →
   Compare. A run executes in the background; this view follows it by polling (useEvalRun)
   and the hook refreshes dashboard and runs when it ends. The dashboard block and the runs
   table fail independently. The compare dialog is a sibling of the table, not part of a row. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { EvalPeriodId } from "@devdigest/shared/constants/evals";
import { ErrorState, Skeleton } from "@devdigest/ui";
import { toggleSelected } from "@/components/eval-run-history";
import { useAgents, useEvalAgentDashboard, useEvalRun, useEvalRuns, useStartEvalRun } from "@/lib/hooks";
import { AGENT_RUNS_LIMIT } from "../../constants";
import { AgentHeader } from "./_components/AgentHeader";
import { AgentMetricTiles } from "./_components/AgentMetricTiles";
import { AgentRunsTable } from "./_components/AgentRunsTable";
import { CompareModal } from "./_components/CompareModal";
import { RegressionBanner } from "./_components/RegressionBanner";
import { TrendChart } from "./_components/TrendChart";
import { comparePair, followedRunId, runningRun } from "./helpers";
import { s } from "./styles";

export function AgentEvalView({ agentId, period }: { agentId: string; period: EvalPeriodId }) {
  const t = useTranslations("eval.agentView");
  const td = useTranslations("eval.dashboard");
  const tc = useTranslations("common");
  const agents = useAgents();
  const dashboard = useEvalAgentDashboard(agentId, period);
  const runs = useEvalRuns(agentId, AGENT_RUNS_LIMIT, period);
  const start = useStartEvalRun(agentId);
  const followed = useEvalRun(followedRunId(runs.data, start.data?.id));
  const [selected, setSelected] = React.useState<string[]>([]);
  const [comparing, setComparing] = React.useState(false);

  const data = dashboard.data;
  const running = runningRun(followed.data, runs.data);
  const pair = comparePair(runs.data, selected);
  const noCases = data != null && data.cases_total === 0;
  const busy = start.isPending || running != null;

  return (
    <div style={s.wrap}>
      <AgentHeader
        agentId={agentId}
        period={period}
        agents={agents.data}
        casesTotal={data?.cases_total}
        runCount={runs.data?.length}
        running={running ? { finished: running.cases_finished, total: running.cases_total } : null}
        runDisabled={dashboard.isLoading || noCases || busy}
        noCases={noCases}
        onRun={() => start.mutate()}
      />

      {data?.alert && data.latest && <RegressionBanner alert={data.alert} latest={data.latest} previous={data.previous} />}

      {dashboard.isLoading ? (
        <Skeleton height={96} />
      ) : dashboard.isError || !data ? (
        <ErrorState title={t("loadError")} onRetry={() => dashboard.refetch()} retryLabel={tc("actions.retry")} />
      ) : data.latest ? (
        <>
          <AgentMetricTiles latest={data.latest} previous={data.previous} history={data.trend} />
          <TrendChart runs={data.trend} period={period} />
        </>
      ) : (
        <p style={s.caption}>{td("noRuns")}</p>
      )}

      <AgentRunsTable
        runs={runs.data}
        state={runs.isError ? "error" : runs.isLoading ? "loading" : "ready"}
        onRetry={() => runs.refetch()}
        selection={{ selectedIds: selected, onToggle: (id) => setSelected((ids) => toggleSelected(ids, id)) }}
        onCompare={pair ? () => setComparing(true) : undefined}
      />

      {comparing && pair && <CompareModal aId={pair[0].id} bId={pair[1].id} onClose={() => setComparing(false)} />}
    </div>
  );
}
