/* CiRunsView — /ci-runs: agent reviews that ran in GitHub Actions or GitLab CI. The studio is local, so
   results arrive only when the user presses Refresh (pull-based ingest). Artifact-derived
   text (agent, model) and repository names are rendered as plain text, never as markup. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { CiRun } from "@devdigest/shared";
import { Badge, Button, EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { CostText } from "@/components/cost-text";
import { codeHostName, isGitLab } from "@/lib/code-host";
import {
  formatRanAt,
  gitlabRefreshErrorCode,
  pullRequestHref,
  pullRequestRef,
  refreshFailures,
  type RefreshFailure,
  runRepoLabel,
  safeRunHref,
  statusKey,
} from "@/lib/ci-runs";
import { useCiRuns, useRefreshCi } from "@/lib/hooks";
import { formatDuration } from "./helpers";
import { s } from "./styles";

export function CiRunsView() {
  const t = useTranslations("ci");
  const tc = useTranslations("common");
  const runs = useCiRuns();
  const refresh = useRefreshCi();
  const failures = refreshFailures(refresh.data);

  return (
    <AppShell crumb={[{ label: t("page.crumb") }]}>
      <div style={s.page}>
        <div style={s.header}>
          <div style={s.headerText}>
            <h1 style={s.h1}>{t("runs.title")}</h1>
            <p style={s.subtitle}>{t("runs.subtitle")}</p>
          </div>
          {refresh.data && <span style={s.result}>{t("runs.refreshResult", { count: refresh.data.ingested })}</span>}
          <Button
            kind="secondary"
            icon="RefreshCw"
            loading={refresh.isPending}
            disabled={refresh.isPending}
            onClick={() => refresh.mutate(undefined)}
          >
            {refresh.isPending ? t("runs.refreshing") : t("runs.refresh")}
          </Button>
        </div>

        {refresh.data?.degraded && (
          <div role="alert" style={s.banner}>
            {t("runs.degradedBanner")}
            <ul style={s.bannerList}>
              {failures.map((f) => (
                <RefreshFailureItem key={f.installationId} failure={f} />
              ))}
            </ul>
          </div>
        )}

        {runs.isLoading && <Skeleton height={220} />}
        {runs.isError && <ErrorState title={t("runs.loadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />}
        {runs.data && runs.data.length === 0 && <EmptyState icon="Workflow" title={t("runs.emptyTitle")} body={t("runs.emptyBody")} />}
        {runs.data && runs.data.length > 0 && <RunsTable runs={runs.data} />}
      </div>
    </AppShell>
  );
}

function RefreshFailureItem({ failure }: { failure: RefreshFailure }) {
  const t = useTranslations("runs.ciRuns.refreshError");
  const known = gitlabRefreshErrorCode(failure.code);
  return (
    <li>
      {failure.repo} — {known ? t(known) : <span className="mono">{failure.code}</span>}
    </li>
  );
}

function RunsTable({ runs }: { runs: readonly CiRun[] }) {
  const t = useTranslations("ci.runs");
  const columns = ["timestamp", "repository", "pullRequest", "agent", "verdict", "findings", "cost", "duration", "status", "run"] as const;
  return (
    <div style={s.scroll}>
      <table style={s.table} aria-label={t("title")}>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c} style={s.th} scope="col">
                {t(`table.${c}`)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <RunRow key={run.id} run={run} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RunRow({ run }: { run: CiRun }) {
  const t = useTranslations("ci.runs");
  const tr = useTranslations("runs.ciRuns");
  const prHref = pullRequestHref(run);
  // A GitLab run keeps its `!<n>` as text when the host is not connected (no link, EC34); GitHub shows "—" without a link.
  const prLabel = isGitLab(run) || prHref ? pullRequestRef(run) : null;
  const runHref = safeRunHref(run.github_url);
  const status = statusKey(run.status);
  return (
    <tr>
      <td className="mono tnum" style={{ ...s.td, ...s.stamp }}>
        {formatRanAt(run.ran_at)}
      </td>
      <td style={s.td}>{runRepoLabel(run) ?? "—"}</td>
      <td style={s.td}>
        {prLabel == null ? (
          <span style={s.muted}>—</span>
        ) : prHref ? (
          <a href={prHref} target="_blank" rel="noopener noreferrer" style={s.link}>
            {prLabel}
          </a>
        ) : (
          <span style={s.muted}>{prLabel}</span>
        )}
      </td>
      <td style={s.td}>{run.agent ?? <span style={s.muted}>{t("agentDeleted")}</span>}</td>
      <td style={s.td}>
        <VerdictBadge verdict={run.verdict} />
      </td>
      <td className="mono tnum" style={s.td}>
        {run.findings_count ?? "—"}
      </td>
      <td style={s.td}>
        <CostText usd={run.cost_usd} />
      </td>
      <td className="mono tnum" style={s.td}>
        {formatDuration(run.duration_s)}
      </td>
      <td style={s.td}>{status ? t(`status.${status}`) : "—"}</td>
      <td style={s.td}>
        {runHref ? (
          <a href={runHref} target="_blank" rel="noopener noreferrer" style={s.link}>
            {isGitLab(run) ? tr("viewOn", { host: codeHostName(run) }) : t("view")}
          </a>
        ) : (
          <span style={s.muted}>—</span>
        )}
      </td>
    </tr>
  );
}

function VerdictBadge({ verdict }: { verdict: CiRun["verdict"] }) {
  const t = useTranslations("ci.runs.verdict");
  if (verdict === "blocked") return <Badge color="var(--crit)" bg="var(--crit-bg)">{t("blocked")}</Badge>;
  if (verdict === "passed") return <Badge color="var(--ok)" bg="var(--ok-bg)">{t("passed")}</Badge>;
  return <span style={s.muted}>—</span>;
}
