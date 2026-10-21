/* ConfigureRunView — /multi-agent/new: pick a PR and 2+ agents, see the estimated time and
   cost, start a multi-agent run (specs/multi-agent-review.md FR15, §9). The PR and agents can be
   preselected from the URL; starting navigates to the results page. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button, Checkbox, EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { Select } from "@/components/select";
import { formatUsd } from "@/lib/format-usage";
import { formatSeconds } from "./helpers";
import { s } from "./styles";
import { useConfigureRun } from "./useConfigureRun";

export function ConfigureRunView({ prId, agentIds }: { prId: string | null; agentIds: string[] | null }) {
  const t = useTranslations("runs.multiAgent.configure");
  const tc = useTranslations("common");
  const router = useRouter();
  const c = useConfigureRun({ prId, agentIds });
  const count = c.selectedIds.length;

  const prOptions = React.useMemo(
    () => [
      { value: "", label: t("prPlaceholder") },
      ...c.pulls.flatMap((p) => (p.id ? [{ value: p.id, label: t("prItem", { number: p.number, title: p.title }) }] : [])),
    ],
    [c.pulls, t],
  );

  const footer =
    c.totals.durationMs === null
      ? t("footerNone")
      : c.totals.costUsd === null
        ? t("footerTimeOnly", { time: formatSeconds(c.totals.durationMs) })
        : t("footer", { time: formatSeconds(c.totals.durationMs), cost: formatUsd(c.totals.costUsd) });

  return (
    <AppShell crumb={[{ label: t("crumb") }]}>
      <div style={s.page}>
        <div style={s.header}>
          <h1 style={s.h1}>{t("title")}</h1>
          <p style={s.subtitle}>{t("subtitle")}</p>
        </div>

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
            <section style={s.section} aria-label={t("prStep")}>
              <div style={s.sectionHead}>
                <h2 style={s.h2}>{t("prStep")}</h2>
              </div>
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
                    aria-label={t("prSelectLabel")}
                  />
                </div>
              )}
            </section>

            <section style={s.section} aria-label={t("agentsStep")}>
              <div style={s.sectionHead}>
                <h2 style={s.h2}>{t("agentsStep")}</h2>
                {c.selectedPr && c.agents.length > 0 && (
                  <>
                    <Button kind="ghost" size="sm" onClick={c.selectAll}>
                      {t("selectAll")}
                    </Button>
                    <Button kind="ghost" size="sm" onClick={c.clear} disabled={count === 0}>
                      {t("clear")}
                    </Button>
                  </>
                )}
              </div>
              {!c.selectedPr && <p style={s.caption}>{t("pickPrFirst")}</p>}
              {c.selectedPr && c.agentsLoading && <Skeleton height={120} />}
              {c.selectedPr && c.agentsError && (
                <ErrorState title={t("agentsError")} onRetry={() => c.refetchAgents()} retryLabel={tc("actions.retry")} />
              )}
              {c.selectedPr && !c.agentsLoading && !c.agentsError && c.agents.length === 0 && (
                <p style={s.caption}>{t("noAgents")}</p>
              )}
              {c.selectedPr && c.agents.length > 0 && (
                <div style={s.cards}>
                  {c.agents.map((a) => {
                    const est = c.estimates.get(a.id);
                    const time = est?.avg_duration_ms != null ? formatSeconds(est.avg_duration_ms) : null;
                    const cost = est?.avg_cost_usd != null ? formatUsd(est.avg_cost_usd) : null;
                    return (
                      <div key={a.id} style={s.card}>
                        <Checkbox
                          checked={c.selected.has(a.id)}
                          onChange={() => c.toggle(a.id)}
                          label={
                            <span style={s.cardLabel}>
                              <span style={s.cardName}>{a.enabled ? a.name : `${a.name} · ${t("agentDisabled")}`}</span>
                              {a.description && <span style={s.cardDesc}>{a.description}</span>}
                            </span>
                          }
                        />
                        <span className="mono tnum" style={s.estimate}>
                          {time === null && cost === null
                            ? t("noEstimate")
                            : t("estimate", { time: time ?? t("noEstimate"), cost: cost ?? t("noEstimate") })}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </section>

            <div style={s.footer}>
              <Button kind="primary" size="md" loading={c.isStarting} disabled={!c.canStart} onClick={c.start}>
                {c.isStarting ? t("running") : t("run", { count })}
              </Button>
              <p style={s.footerText}>{footer}</p>
              {c.selectedPr && count < 2 && <p style={s.caption}>{t("minAgents")}</p>}
            </div>
          </>
        )}
      </div>
    </AppShell>
  );
}
