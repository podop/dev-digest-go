/* RunHeader — "Configure run" (back to Configure run with this PR and agents), the page title,
   "N selected agents · parallel" and the Columns/Tabs toggle; below it the PR number + title
   (link to the PR page) and "N agents · fan-out via worktrees · time total · cost"
   (specs/multi-agent-review.md FR16; Claude Design screen_multiagent.jsx MetaRow). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { MultiAgentRun } from "@devdigest/shared";
import { formatUsd } from "@/lib/format-usage";
import type { ResultsView } from "../../constants";
import { configurePath, prPagePath } from "../../helpers";
import { formatSeconds } from "../RunMeta/helpers";
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
  const prTitle = run.pr_number != null ? (run.pr_title ?? "") : t("unknownPr");

  return (
    <header>
      <div style={s.titleRow}>
        <Link href={configurePath(run.pr_id, run.columns)} title={t("changeRun")} style={s.configure}>
          <Icon.Settings size={14} aria-hidden />
          {t("configureRun")}
        </Link>
        <h1 style={s.h1}>{t("heading")}</h1>
        <span style={s.selected}>{t("selectedAgents", { count: run.agent_count })}</span>
        <div style={s.toggle} role="group" aria-label={t("viewLabel")}>
          {VIEWS.map((v) => (
            <button key={v} type="button" aria-pressed={view === v} onClick={() => onViewChange(v)} style={s.toggleBtn(view === v)}>
              {t(`view.${v}`)}
            </button>
          ))}
        </div>
      </div>
      <div style={s.metaRow}>
        {run.pr_number != null && (
          <span className="mono" style={s.prNumber}>
            #{run.pr_number}
          </span>
        )}
        {prHref ? (
          <Link href={prHref} style={s.prTitle}>
            {prTitle}
          </Link>
        ) : (
          <span style={s.prTitle}>{prTitle}</span>
        )}
        <span style={s.fanOut}>
          <Icon.Cpu size={14} aria-hidden style={s.fanOutIcon} />
          {t("fanOut", {
            count: run.agent_count,
            time: formatSeconds(run.total_duration_ms),
            cost: run.total_cost_usd == null ? "—" : formatUsd(run.total_cost_usd),
          })}
        </span>
      </div>
    </header>
  );
}
