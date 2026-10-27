/* MultiAgentRunView — /multi-agent/:id: the parallel run's agents side by side (Columns or Tabs),
   live status while any column runs, "Where agents disagree", and the trace drawer for one agent
   run (specs/multi-agent-review.md FR16–FR21, §9). The read model is polled by useMultiAgentRun. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { RunTraceDrawer } from "@/components/run-trace-drawer";
import { ApiError } from "@/lib/api";
import { useMultiAgentRun } from "@/lib/hooks";
import type { ResultsView } from "./constants";
import { countNotCompared } from "./helpers";
import { useResultsUrl } from "./useResultsUrl";
import { AgentColumns } from "./_components/AgentColumns";
import { AgentTabs } from "./_components/AgentTabs";
import { DisagreementBlock } from "./_components/DisagreementBlock";
import { RunHeader } from "./_components/RunHeader";
import { s } from "./styles";

export function MultiAgentRunView({
  id,
  view,
  traceRunId,
}: {
  id: string;
  view: ResultsView;
  traceRunId: string | null;
}) {
  const t = useTranslations("runs.multiAgent.results");
  const tc = useTranslations("common");
  const tl = useTranslations("runs.multiAgent.list");
  const run = useMultiAgentRun(id);
  const url = useResultsUrl(id, view, traceRunId);
  const data = run.data;
  const crumb = [
    { label: tl("crumb"), href: "/multi-agent" },
    data?.pr_number != null ? { label: `#${data.pr_number}`, mono: true } : { label: t("crumb") },
  ];

  if (run.isError && run.error instanceof ApiError && run.error.status === 404) {
    return (
      <AppShell crumb={crumb}>
        <div style={s.state}>
          <ErrorState title={t("notFound")} />
          <Link href="/multi-agent" style={s.back}>
            {t("backToList")}
          </Link>
        </div>
      </AppShell>
    );
  }

  // The drawer needs `running` before it mounts (it picks its first tab once), so it waits for the data.
  const traceColumn = data && traceRunId ? data.columns.find((c) => c.run_id === traceRunId) : undefined;

  // Hidden until the first column is done; once nothing is still running (e.g. every column failed) it shows too.
  const showDisagreement =
    !!data &&
    data.columns.length > 0 &&
    (data.columns.some((c) => c.status === "done") || data.columns.every((c) => c.status !== "running"));

  return (
    <AppShell crumb={crumb}>
      <div style={s.page}>
        {run.isLoading && (
          <div style={s.skeletons}>
            <Skeleton height={56} />
            <Skeleton height={240} />
          </div>
        )}
        {run.isError && !data && (
          <div style={s.state}>
            <ErrorState title={t("loadError")} onRetry={() => run.refetch()} retryLabel={tc("actions.retry")} />
          </div>
        )}
        {data && (
          <>
            <RunHeader run={data} view={view} onViewChange={url.setView} />
            {view === "tabs" ? (
              <AgentTabs run={data} onViewTrace={url.openTrace} />
            ) : (
              <AgentColumns columns={data.columns} onViewTrace={url.openTrace} />
            )}
            {showDisagreement && (
              <div style={s.disagreement}>
                <DisagreementBlock groups={data.conflicts} notComparedCount={countNotCompared(data.columns)} />
              </div>
            )}
          </>
        )}
      </div>
      {data && traceColumn && (
        <RunTraceDrawer
          key={traceColumn.run_id}
          runId={traceColumn.run_id}
          running={traceColumn.status === "running"}
          prNumber={data.pr_number ?? null}
          findings={traceColumn.findings}
          agentName={traceColumn.agent_name || t("deletedAgent")}
          onClose={url.closeTrace}
        />
      )}
    </AppShell>
  );
}
