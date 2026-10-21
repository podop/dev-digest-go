/* BlastRadiusCard — "what else can this diff hit?" (server/specs/07-blast-radius.md,
   client/specs/07-blast-radius.md): one inline stat row + Tree/Graph switch, symbol tree, endpoint/cron chips.
   Read from GET /pulls/:id/blast (an index read). A degraded answer is shown as
   "unknown", never as an empty "no impact" — with a Resync button. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Button, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useBlastRadius, useResyncBlast } from "@/lib/hooks/blast";
import { BlastGraph } from "./_components/BlastGraph";
import { BlastTree } from "./_components/BlastTree";
import { PriorPrs } from "./_components/PriorPrs";
import { STAT_ICON, STAT_KEYS, VIEWS } from "./constants";
import { blastStats, callerHref, degradedReasonKey } from "./helpers";
import { s } from "./styles";

export interface BlastRadiusCardProps {
  prId: string;
  repoId: string;
  repoFullName: string | null;
  headSha: string | null;
}

export function BlastRadiusCard({ prId, repoId, repoFullName, headSha }: BlastRadiusCardProps) {
  const t = useTranslations("blast");
  const tc = useTranslations("common");
  const { data, isLoading, isError, error, refetch } = useBlastRadius(prId);
  const resync = useResyncBlast(repoId, prId);
  const [view, setView] = React.useState<(typeof VIEWS)[number]>("tree");

  if (isLoading) return <Skeleton height={140} />;
  if (isError || !data) {
    return (
      <ErrorState
        title={t("error.title")}
        body={error instanceof ApiError ? error.message : t("error.body")}
        onRetry={() => refetch()}
        retryLabel={tc("actions.retry")}
      />
    );
  }

  const stats = blastStats(data);
  const degraded = data.degraded === true;

  return (
    <section style={s.card} aria-label={t("title")}>
      <div style={s.header}>
        <Icon.Workflow size={14} style={s.headerIcon} />
        <span style={s.headerTitle}>{t("title")}</span>
      </div>

      <div style={s.summary}>
        <ul style={s.stats}>
          {STAT_KEYS.map((key) => {
            const StatIcon = Icon[STAT_ICON[key]];
            return (
              <li key={key} style={s.stat}>
                <StatIcon size={13} style={s.statIcon} />
                <b className="tnum" style={s.statValue}>
                  {stats[key]}
                </b>
                {t(`stat.${key}`, { count: stats[key] })}
              </li>
            );
          })}
        </ul>
        <div style={s.segmented} role="group" aria-label={t("view.label")}>
          {VIEWS.map((v) => (
            <button key={v} type="button" aria-pressed={view === v} onClick={() => setView(v)} style={s.segment(view === v)}>
              {t(`view.${v}`)}
            </button>
          ))}
        </div>
      </div>

      {degraded && (
        <div style={s.degraded} role="status">
          <Badge icon="AlertTriangle" color="var(--warn)" bg="transparent">
            {t("degraded.badge")}
          </Badge>
          <span style={s.degradedText}>
            {t(`degraded.reason.${degradedReasonKey(data.reason)}`)} {t("degraded.notImpact")}
            {resync.isSuccess && ` ${t("degraded.resyncStarted")}`}
          </span>
          <Button kind="secondary" size="sm" icon="RefreshCw" loading={resync.isPending} onClick={() => resync.mutate()}>
            {t("degraded.resync")}
          </Button>
        </div>
      )}

      {view === "graph" ? (
        <BlastGraph downstream={data.downstream} />
      ) : data.downstream.length > 0 ? (
        <BlastTree downstream={data.downstream} callerHref={(c) => callerHref(repoFullName, headSha, c)} />
      ) : (
        !degraded && (
          <p style={s.muted}>{stats.symbols > 0 ? t("noDownstream", { count: stats.symbols }) : t("noSymbols")}</p>
        )
      )}

      <PriorPrs prId={prId} repoFullName={repoFullName} />
    </section>
  );
}
