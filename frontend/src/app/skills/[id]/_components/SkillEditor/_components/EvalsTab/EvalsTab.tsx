/* EvalsTab (skill) — the skill's eval cases and results: latest-run metric tiles with deltas, the shared
   case list ("n / m passing", editor, delete), a "Run with" host-agent picker, "Run all evals" with live
   progress and the run history with skill version and host agent (FR23, FR24). A skill run executes the
   skill's cases on the chosen agent with only this skill attached; the tab follows it by polling
   (useEvalRun) and the hook refreshes tiles, rows and history when it ends. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Skeleton } from "@devdigest/ui";
import type { Skill } from "@devdigest/shared";
import { EvalCasesSection } from "@/components/eval-cases";
import { MetricTiles } from "@/components/eval-metrics";
import { followedRunId, RunsTable, runningRun } from "@/components/eval-run-history";
import { useAgents, useEvalRun, useSkillAgents, useSkillEvalCases, useSkillEvalRuns, useStartSkillEvalRun } from "@/lib/hooks";
import { HostAgentPicker } from "./_components/HostAgentPicker";
import { effectiveHostAgentId, latestFinishedPair, runAllBlocker } from "./helpers";
import { s } from "./styles";

export function EvalsTab({ skill }: { skill: Skill }) {
  const t = useTranslations("eval.skillEvals");
  const ta = useTranslations("eval.agentEvals");
  const tc = useTranslations("common");
  const cases = useSkillEvalCases(skill.id);
  const runs = useSkillEvalRuns(skill.id);
  const agents = useAgents();
  const usedBy = useSkillAgents(skill.id);
  const start = useStartSkillEvalRun(skill.id);
  const followed = useEvalRun(followedRunId(runs.data, start.data?.id));
  const [picked, setPicked] = React.useState<string | null>(null);

  const agentList = agents.data ?? [];
  const hostId = effectiveHostAgentId(agentList, usedBy.data, picked);
  const running = runningRun(followed.data, runs.data);
  const { latest, previous } = latestFinishedPair(runs.data);
  const noCases = !cases.isLoading && !cases.isError && cases.data?.length === 0;
  const noAgents = !agents.isLoading && !agents.isError && agentList.length === 0;
  const blocker = runAllBlocker({ noCases, noAgents });
  const busy = start.isPending || running != null;
  const loading = cases.isLoading || cases.isError || agents.isLoading || agents.isError || usedBy.isLoading;

  const runSlot = (
    <div style={s.runControls}>
      <HostAgentPicker agents={agentList} value={hostId} onChange={setPicked} disabled={agents.isLoading || busy} />
      <span style={s.gridItem} title={blocker ? t(blocker === "noCases" ? "runAllNoCases" : "runAllNoAgents") : undefined}>
        <Button kind="primary" icon="Play" disabled={loading || blocker != null || hostId == null || busy} onClick={() => hostId && start.mutate({ agent_id: hostId })}>
          {running ? ta("runningProgress", { finished: running.cases_finished, total: running.cases_total }) : ta("runAll")}
        </Button>
      </span>
    </div>
  );

  return (
    <div style={s.wrap}>
      <section style={s.section} aria-label={ta("metricsTitle")}>
        <div style={s.titleBlock}>
          <h2 style={s.h2}>{ta("metricsTitle")}</h2>
          <p style={s.subtitle}>{t("metricsSubtitle")}</p>
        </div>
        {runs.isLoading ? (
          <Skeleton height={96} />
        ) : runs.isError ? (
          <ErrorState title={ta("metricsLoadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />
        ) : latest ? (
          <MetricTiles latest={latest} previous={previous} showCasesPassed />
        ) : (
          <p style={s.caption}>{t("noRuns")}</p>
        )}
      </section>

      <EvalCasesSection owner={{ kind: "skill", id: skill.id }} runSlot={runSlot} emptyText={t("emptyCases")} />

      <section style={s.section} aria-label={ta("historyHeading")}>
        <h2 style={s.h2}>{ta("historyHeading")}</h2>
        {runs.isError ? (
          <ErrorState title={ta("historyLoadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />
        ) : runs.data && runs.data.length > 0 ? (
          <RunsTable runs={runs.data} variant="skill" />
        ) : runs.isLoading ? (
          <Skeleton height={80} />
        ) : (
          <p style={s.caption}>{t("noRuns")}</p>
        )}
      </section>
    </div>
  );
}
