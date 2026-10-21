/* AgentHeader — the agent view's header: back link, the agent's name with its model badge,
   the "Regression harness" subtitle, the link to the agent's eval cases, and the toolbar —
   agent picker, period control, Run eval (with live "Running x / N"). Picker and period
   write the URL (the view's only state); the run itself belongs to the caller. */
"use client";

import React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import type { Agent } from "@devdigest/shared";
import { EVAL_PERIODS, type EvalPeriodId } from "@devdigest/shared/constants/evals";
import { Select } from "@/components/select";
import { AGENT_RUNS_LIMIT } from "@/app/eval/constants";
import { agentEvalsHref, evalHref } from "@/app/eval/helpers";
import { s } from "./styles";

export interface AgentHeaderProps {
  agentId: string;
  period: EvalPeriodId;
  agents: readonly Agent[] | undefined;
  /** Eval cases of the agent; undefined until the dashboard has loaded. */
  casesTotal: number | undefined;
  /** Runs of the period (the list length, capped at the request limit); undefined until loaded. */
  runCount: number | undefined;
  running: { finished: number; total: number } | null;
  /** Run eval is unavailable (loading, no cases, or a run is already going). */
  runDisabled: boolean;
  noCases: boolean;
  onRun: () => void;
}

export function AgentHeader({ agentId, period, agents, casesTotal, runCount, running, runDisabled, noCases, onRun }: AgentHeaderProps) {
  const t = useTranslations("eval.agentView");
  const td = useTranslations("eval.dashboard");
  const router = useRouter();
  const agent = agents?.find((a) => a.id === agentId);
  const name = agent?.name ?? td("defaultTitle");
  const agentOptions = React.useMemo(() => (agents ?? []).map((a) => ({ value: a.id, label: a.name })), [agents]);
  const periodOptions = React.useMemo(() => EVAL_PERIODS.map((p) => ({ value: p, label: t(`period.${p}`) })), [t]);
  const runsLabel =
    runCount == null ? null : runCount >= AGENT_RUNS_LIMIT ? t("runsCapped", { count: AGENT_RUNS_LIMIT }) : t("runs", { count: runCount });

  return (
    <div style={s.wrap}>
      <Link href={evalHref()} style={s.back}>
        <Icon.ChevronLeft size={14} aria-hidden />
        {t("back")}
      </Link>

      <header style={s.header}>
        <div style={s.titleBlock}>
          <div style={s.titleRow}>
            <h1 style={s.h1} title={name}>
              {name}
            </h1>
            {agent?.model && (
              <span className="mono" style={s.model}>
                {agent.model}
              </span>
            )}
          </div>
          <div style={s.meta}>
            {runsLabel != null && casesTotal != null && <p style={s.subtitle}>{t("subtitle", { runs: runsLabel, cases: casesTotal })}</p>}
            <Link href={agentEvalsHref(agentId)} style={s.link}>
              {td("configure")}
            </Link>
          </div>
        </div>

        <div style={s.toolbar}>
          <div style={s.picker}>
            <Select
              value={agentId}
              options={agentOptions}
              mono={false}
              icon="Cpu"
              chevron="down"
              aria-label={t("pickerLabel")}
              disabled={agentOptions.length === 0}
              onChange={(id) => router.replace(evalHref(id, period))}
            />
          </div>
          <div style={s.period}>
            <Select
              value={period}
              options={periodOptions}
              mono={false}
              icon="Calendar"
              chevron="none"
              aria-label={t("periodLabel")}
              onChange={(p) => router.replace(evalHref(agentId, p))}
            />
          </div>
          <span style={s.run} title={noCases ? t("noCasesHint") : undefined}>
            <Button kind="primary" icon="Play" disabled={runDisabled} onClick={onRun}>
              {running ? t("runningProgress", { finished: running.finished, total: running.total }) : t("runEval")}
            </Button>
          </span>
        </div>
      </header>
    </div>
  );
}
