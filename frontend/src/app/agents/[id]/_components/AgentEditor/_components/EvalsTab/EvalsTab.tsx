/* EvalsTab — the agent's eval cases and results: latest-run metric tiles with deltas,
   the case list ("n / m passing"), "Run all evals" with live progress, the run history,
   and a link to the full dashboard (FR21). A run executes in the background; this tab
   follows it by polling (useEvalRun) and the hook refreshes tiles, rows and history when
   it ends. The case list, editor and delete confirmation are the shared EvalCasesSection;
   this tab hands it the Run all control. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Skeleton } from "@devdigest/ui";
import type { Agent } from "@devdigest/shared";
import { EvalCasesSection } from "@/components/eval-cases";
import { MetricTiles } from "@/components/eval-metrics";
import { followedRunId, RunsTable, runningRun } from "@/components/eval-run-history";
import { useEvalAgentDashboard, useEvalCases, useEvalRun, useEvalRuns, useStartEvalRun } from "@/lib/hooks";
import { dashboardHref } from "./helpers";
import { s } from "./styles";

export function EvalsTab({ agent }: { agent: Agent }) {
  const t = useTranslations("eval.agentEvals");
  const tc = useTranslations("common");
  const cases = useEvalCases(agent.id);
  const dashboard = useEvalAgentDashboard(agent.id);
  const runs = useEvalRuns(agent.id);
  const start = useStartEvalRun(agent.id);
  const followed = useEvalRun(followedRunId(runs.data, start.data?.id));

  const running = runningRun(followed.data, runs.data);
  const noCases = !cases.isLoading && !cases.isError && cases.data?.length === 0;
  const busy = start.isPending || running != null;

  const runSlot = (
    <span title={noCases ? t("runAllNoCases") : undefined}>
      <Button kind="primary" icon="Play" disabled={cases.isLoading || cases.isError || noCases || busy} onClick={() => start.mutate()}>
        {running ? t("runningProgress", { finished: running.cases_finished, total: running.cases_total }) : t("runAll")}
      </Button>
    </span>
  );

  return (
    <div style={s.wrap}>
      <section style={s.section} aria-label={t("metricsTitle")}>
        <div style={s.header}>
          <div style={s.titleBlock}>
            <h2 style={s.h2}>{t("metricsTitle")}</h2>
            <p style={s.subtitle}>{t("metricsSubtitle")}</p>
          </div>
          <Link href={dashboardHref(agent.id)} style={s.link}>
            {t("viewDashboard")}
          </Link>
        </div>
        {dashboard.isLoading ? (
          <Skeleton height={96} />
        ) : dashboard.isError || !dashboard.data ? (
          <ErrorState title={t("metricsLoadError")} onRetry={() => dashboard.refetch()} retryLabel={tc("actions.retry")} />
        ) : dashboard.data.latest ? (
          <MetricTiles latest={dashboard.data.latest} previous={dashboard.data.previous} showCasesPassed />
        ) : (
          <p style={s.caption}>{t("noRuns")}</p>
        )}
      </section>

      <EvalCasesSection owner={{ kind: "agent", id: agent.id }} runSlot={runSlot} emptyText={t("emptyCases")} />

      <section style={s.section} aria-label={t("historyHeading")}>
        <h2 style={s.h2}>{t("historyHeading")}</h2>
        {runs.isError ? (
          <ErrorState title={t("historyLoadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />
        ) : runs.data && runs.data.length > 0 ? (
          <RunsTable runs={runs.data} />
        ) : (
          runs.isLoading && <Skeleton height={80} />
        )}
      </section>

    </div>
  );
}
