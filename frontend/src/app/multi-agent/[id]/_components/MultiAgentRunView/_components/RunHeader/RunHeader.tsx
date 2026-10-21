/* RunHeader — PR number + title (link to the PR page), "N agents · parallel", total time and cost,
   the "Configure run" link and the Columns/Tabs toggle (specs/multi-agent-review.md FR16). */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import type { MultiAgentRun } from "@devdigest/shared";
import type { ResultsView } from "../../constants";
import { configurePath, prPagePath } from "../../helpers";
import { RunMeta } from "../RunMeta";
import { s } from "./styles";

const VIEWS: readonly ResultsView[] = ["columns", "tabs"];

export function RunHeader({
  run,
  view,
  onViewChange,
}: {
  run: MultiAgentRun;
  view: ResultsView;
  onViewChange: (view: ResultsView) => void;
}) {
  const t = useTranslations("runs.multiAgent.results");
  const prHref = prPagePath(run.repo_id, run.pr_number);
  const prLabel = run.pr_number != null ? t("prLabel", { number: run.pr_number, title: run.pr_title ?? "" }) : t("unknownPr");

  return (
    <header style={s.header}>
      <div style={s.titleRow}>
        <h1 style={s.h1}>
          {prHref ? (
            <Link href={prHref} style={s.prLink}>
              {prLabel}
            </Link>
          ) : (
            prLabel
          )}
        </h1>
        <div style={s.toggle} role="group" aria-label={t("viewLabel")}>
          {VIEWS.map((v) => (
            <Button key={v} kind="secondary" size="sm" active={view === v} aria-pressed={view === v} onClick={() => onViewChange(v)}>
              {t(`view.${v}`)}
            </Button>
          ))}
        </div>
      </div>
      <div style={s.metaRow}>
        <span>{t("agentsParallel", { count: run.agent_count })}</span>
        <RunMeta durationMs={run.total_duration_ms} costUsd={run.total_cost_usd} />
        <span style={s.spacer} />
        <Link href={configurePath(run.pr_id, run.columns)} style={s.configure}>
          {t("configureRun")}
        </Link>
      </div>
    </header>
  );
}
