/* ConfigureRunView — /multi-agent/new: pick a PR and 2+ agents, see the estimated time and
   cost, start a multi-agent run (specs/multi-agent-review.md FR15, §9; Claude Design
   screen_multiagent.jsx RunConfig). The PR and agents can be preselected from the URL;
   starting navigates to the results page. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button, EmptyState, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { Select } from "@/components/select";
import { formatUsd } from "@/lib/format-usage";
import { AgentPickCard } from "./_components/AgentPickCard";
import { formatSeconds } from "./helpers";
import { s } from "./styles";
import { useConfigureRun } from "./useConfigureRun";

function StepHead({ n, active, children, right }: { n: number; active: boolean; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div style={s.stepHead}>
      <span aria-hidden style={s.stepNum(active)}>
        {n}
      </span>
      <h2 style={s.stepTitle(active)}>{children}</h2>
      {right}
    </div>
  );
}

export function ConfigureRunView({ prId, agentIds }: { prId: string | null; agentIds: string[] | null }) {
  const t = useTranslations("runs.multiAgent.configure");
  const tl = useTranslations("runs.multiAgent.list");
  const tc = useTranslations("common");
  const router = useRouter();
  const c = useConfigureRun({ prId, agentIds });
  const count = c.selectedIds.length;
  const hasPr = !!c.selectedPr;
  const allOn = c.agents.length > 0 && count === c.agents.length;

  const prOptions = React.useMemo(
    () => [
      { value: "", label: t("prPlaceholder") },
      ...c.pulls.flatMap((p) => (p.id ? [{ value: p.id, label: t("prItem", { number: p.number, title: p.title }) }] : [])),
    ],
    [c.pulls, t],
  );

  const estimate =
    c.totals.durationMs === null
      ? t("footerNone")
      : c.totals.costUsd === null
        ? t("footerTimeOnly", { time: formatSeconds(c.totals.durationMs) })
        : t("footer", { time: formatSeconds(c.totals.durationMs), cost: formatUsd(c.totals.costUsd) });

  return (
    <AppShell crumb={[{ label: tl("crumb"), href: "/multi-agent" }, { label: t("crumb") }]}>
      <div style={s.page}>
        <h1 style={s.h1}>{t("title")}</h1>
        <p style={s.subtitle}>{t("subtitle")}</p>

        {c.reposLoaded && !c.hasRepo ? (
          <EmptyState
            icon="GitBranch"
            title={t("noRepoTitle")}
            body={t("noRepoBody")}
            cta={t("noRepoCta")}
            onCta={() => router.push("/onboarding")}
          />
        ) : (
          <>
            <section aria-label={t("prStep")}>
              <StepHead n={1} active>
                {t("prStep")}
              </StepHead>
              <div style={s.stepBody}>
                {c.pullsLoading && <Skeleton height={34} />}
                {c.pullsError && (
                  <ErrorState title={t("pullsError")} onRetry={() => c.refetchPulls()} retryLabel={tc("actions.retry")} />
                )}
                {!c.pullsLoading && !c.pullsError && c.hasRepo && c.pulls.length === 0 && (
                  <EmptyState icon="GitPullRequest" title={t("noPulls")} body={t("noPullsBody")} />
                )}
                {c.pulls.length > 0 && (
                  <div style={s.prSelect}>
                    <Select
                      value={c.selectedPr}
                      onChange={c.setSelectedPr}
                      options={prOptions}
                      mono={false}
                      icon="GitPullRequest"
                      chevron="down"
                      compact
                      aria-label={t("prSelectLabel")}
                    />
                  </div>
                )}
              </div>
            </section>

            <section aria-label={t("agentsStep")}>
              <StepHead
                n={2}
                active={hasPr}
                right={
                  hasPr &&
                  c.agents.length > 0 && (
                    <button type="button" onClick={allOn ? c.clear : c.selectAll} style={s.selectAll}>
                      {allOn ? t("clearAll") : t("selectAll")}
                    </button>
                  )
                }
              >
                {t("agentsStep")}
              </StepHead>
              <div style={s.agentsBody}>
                {!hasPr && (
                  <div style={s.pickFirst}>
                    <div style={s.pickFirstIcon}>
                      <Icon.GitPullRequest size={21} aria-hidden />
                    </div>
                    <div style={s.pickFirstTitle}>{t("pickPrFirst")}</div>
                    <p style={s.pickFirstBody}>{t("pickPrFirstBody")}</p>
                  </div>
                )}
                {hasPr && c.agentsLoading && <Skeleton height={120} />}
                {hasPr && c.agentsError && (
                  <ErrorState title={t("agentsError")} onRetry={() => c.refetchAgents()} retryLabel={tc("actions.retry")} />
                )}
                {hasPr && !c.agentsLoading && !c.agentsError && c.agents.length === 0 && <p style={s.caption}>{t("noAgents")}</p>}
                {hasPr &&
                  c.agents.map((a) => {
                    const est = c.estimates.get(a.id);
                    const time = est?.avg_duration_ms != null ? formatSeconds(est.avg_duration_ms) : null;
                    const cost = est?.avg_cost_usd != null ? formatUsd(est.avg_cost_usd) : null;
                    return (
                      <AgentPickCard
                        key={a.id}
                        agent={a}
                        on={c.selected.has(a.id)}
                        onToggle={() => c.toggle(a.id)}
                        estimate={
                          time === null && cost === null
                            ? t("noEstimate")
                            : t("estimate", { time: time ?? t("noEstimate"), cost: cost ?? t("noEstimate") })
                        }
                      />
                    );
                  })}
              </div>
            </section>

            <div style={s.runBar}>
              <Button kind="primary" size="md" icon="Users" loading={c.isStarting} disabled={!c.canStart} onClick={c.start}>
                {c.isStarting ? t("running") : count === 0 ? t("selectAgents") : t("run", { count })}
              </Button>
              {hasPr && count > 0 && (
                <span className="mono" style={s.estimate}>
                  {estimate}
                </span>
              )}
            </div>
            {hasPr && count < 2 && <p style={s.minAgents}>{t("minAgents")}</p>}
          </>
        )}
      </div>
    </AppShell>
  );
}
