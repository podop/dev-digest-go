/* CiTab — the agent's CI deployment: the repositories it is exported to (GitHub Actions or GitLab CI),
   "outdated" when the agent moved past the deployed manifest, Add / Update (opens the Export
   wizard), a manual Refresh that pulls results from GitHub, the newest CI runs and the
   "Fail CI on" gate. Artifact-derived text and repo names render as plain text. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Button, ErrorState, Skeleton } from "@devdigest/ui";
import type { Agent, CiInstallation, CiRun } from "@devdigest/shared";
import { Select } from "@/components/select";
import { codeHostName, isGitLab, repoLabel } from "@/lib/code-host";
import {
  formatRanAt,
  pullRequestHref,
  pullRequestRef,
  refreshFailures,
  runRepoLabel,
  safeRunHref,
  statusKey,
} from "@/lib/ci-runs";
import { CI_TAB_RUNS_LIMIT, useCiInstallations, useCiRuns, useRefreshCi, useUpdateAgent } from "@/lib/hooks";
import { ExportWizard } from "./_components/ExportWizard";
import { CI_FAIL_ON_VALUES } from "./constants";
import { formatRelativeTime, isGitLabInstallation, isOutdated } from "./helpers";
import { s } from "./styles";

/** What opens the Export wizard: a row's "Update" preselects its repo; "Add" leaves it to the wizard. */
export interface ExportWizardRequest {
  repo?: string;
}

export function CiTab({ agent }: { agent: Agent }) {
  const t = useTranslations("ci.ciTab");
  const ta = useTranslations("agents");
  const tc = useTranslations("common");
  const installations = useCiInstallations(agent.id);
  const runs = useCiRuns({ agentId: agent.id, limit: CI_TAB_RUNS_LIMIT });
  const refresh = useRefreshCi();
  const update = useUpdateAgent();
  const [wizard, setWizard] = React.useState<ExportWizardRequest | null>(null);
  const failures = refreshFailures(refresh.data);

  const failOnOptions = CI_FAIL_ON_VALUES.map((v) => ({ value: v, label: ta(`config.ciFailOnOptions.${v}`) }));

  return (
    <div style={s.wrap}>
      <section style={s.section} aria-label={t("heading")}>
        <div style={s.header}>
          <div style={s.titleBlock}>
            <h2 style={s.h2}>{t("heading")}</h2>
            <p style={s.subtitle}>
              {installations.data ? t("activeIn", { count: installations.data.length }) : t("subtitle")}
            </p>
          </div>
          {refresh.data && <span style={s.result}>{t("refreshResult", { count: refresh.data.ingested })}</span>}
          <Button
            kind="secondary"
            icon="RefreshCw"
            loading={refresh.isPending}
            disabled={refresh.isPending}
            onClick={() => refresh.mutate(agent.id)}
          >
            {refresh.isPending ? t("refreshing") : t("refresh")}
          </Button>
          <Button kind="primary" icon="Plus" onClick={() => setWizard({})}>
            {t("addToCi")}
          </Button>
        </div>

        {refresh.data?.degraded && (
          <div role="alert" style={s.banner}>
            {t("degradedBanner")}
            <ul style={s.bannerList}>
              {failures.map((f) => (
                <li key={f.repo}>
                  {f.repo} — <span className="mono">{f.code}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {installations.isLoading && <Skeleton height={64} />}
        {installations.isError && (
          <ErrorState title={t("installationsLoadError")} onRetry={() => installations.refetch()} retryLabel={tc("actions.retry")} />
        )}
        {installations.data && installations.data.length === 0 && (
          <div>
            <p style={s.repo}>{t("notDeployed")}</p>
            <p style={s.caption}>{t("notDeployedBody")}</p>
          </div>
        )}
        {installations.data && installations.data.length > 0 && (
          <ul style={s.list}>
            {installations.data.map((inst) => (
              <li key={inst.id}>
                <InstallationRow inst={inst} agentVersion={agent.version} onUpdate={() => setWizard({ repo: inst.repo })} />
              </li>
            ))}
            <li>
              <button type="button" style={s.addRow} onClick={() => setWizard({})}>
                {t("addRepository")}
              </button>
            </li>
          </ul>
        )}
      </section>

      <section style={s.section} aria-label={t("recentRuns")}>
        <h2 style={s.h2}>{t("recentRuns")}</h2>
        {runs.isLoading && <Skeleton height={80} />}
        {runs.isError && <ErrorState title={t("runsLoadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />}
        {runs.data && runs.data.length === 0 && <p style={s.caption}>{t("noRuns")}</p>}
        {runs.data && runs.data.length > 0 && <RunsTable runs={runs.data} />}
      </section>

      <section style={s.failOn} aria-label={t("failOnLabel")}>
        <span style={s.failOnLabel}>{t("failOnLabel")}</span>
        <Select
          value={agent.ci_fail_on}
          onChange={(v) => update.mutate({ id: agent.id, patch: { ci_fail_on: v } })}
          options={failOnOptions}
          disabled={update.isPending}
          aria-label={t("failOnLabel")}
        />
        <p style={s.caption}>{t("failOnNote")}</p>
      </section>

      {wizard && (
        <ExportWizard
          agentId={agent.id}
          agentName={agent.name}
          provider={agent.provider}
          initialRepo={wizard.repo}
          onClose={() => setWizard(null)}
        />
      )}
    </div>
  );
}

function InstallationRow({ inst, agentVersion, onUpdate }: { inst: CiInstallation; agentVersion: number; onUpdate: () => void }) {
  const t = useTranslations("ci.ciTab");
  const tr = useTranslations("ci.runs");
  const last = inst.last_run;
  const status = statusKey(last?.status);
  const gitlab = isGitLabInstallation(inst);
  return (
    <div style={s.row}>
      <div style={s.rowMain}>
        <span style={s.repo}>{gitlab ? repoLabel({ provider: "gitlab", host: inst.host, full_name: inst.repo }) : inst.repo}</span>
        <span style={s.meta}>{gitlab ? t("targetGitlab") : t("target")}</span>
      </div>
      <span style={s.meta} title={last?.ran_at ? formatRanAt(last.ran_at) : undefined}>
        {last ? `${status ? tr(`status.${status}`) : "—"} · ${formatRelativeTime(last.ran_at)}` : t("noRunsYet")}
      </span>
      {inst.manifest_version != null && (
        <span className="mono" style={s.meta}>
          {t("deployedVersion", { version: inst.manifest_version })}
        </span>
      )}
      {isOutdated(agentVersion, inst.manifest_version) && (
        <Badge color="var(--warn)" bg="var(--warn-bg)">
          {t("outdated")}
        </Badge>
      )}
      <Button kind="secondary" size="sm" onClick={onUpdate}>
        {t("updateRow")}
      </Button>
    </div>
  );
}

function RunsTable({ runs }: { runs: readonly CiRun[] }) {
  const t = useTranslations("ci.runs");
  const columns = ["timestamp", "repository", "pullRequest", "verdict", "findings", "status", "run"] as const;
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
  // A GitLab run keeps its `!<n>` as text when the host is not connected (no link); GitHub shows "—" without a link.
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
      <td style={s.td}>
        {run.verdict === "blocked" ? (
          <Badge color="var(--crit)" bg="var(--crit-bg)">{t("verdict.blocked")}</Badge>
        ) : run.verdict === "passed" ? (
          <Badge color="var(--ok)" bg="var(--ok-bg)">{t("verdict.passed")}</Badge>
        ) : (
          <span style={s.muted}>—</span>
        )}
      </td>
      <td className="mono tnum" style={s.td}>
        {run.findings_count ?? "—"}
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
