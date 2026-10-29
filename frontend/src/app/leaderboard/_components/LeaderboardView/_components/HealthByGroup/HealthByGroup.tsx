/* HealthByGroup — the leaderboard's "Health by author" and "Health by repository" cards: the "Health by PR size"
   bar list per author / repository, worst health first as the API orders them, top 8 until "Show all N". A group
   whose PRs have no scored review shows "—" and an empty track but keeps its PR and findings counts. */
"use client";

import * as React from "react";
import { useTranslations } from "next-intl";
import type { LeaderboardAuthorHealth, LeaderboardRepoHealth } from "@devdigest/shared";
import { Avatar, Card } from "@devdigest/ui";
import { healthColor, NO_REPOS, type RepoLookup, repoNameLabel } from "../../helpers";
import { s } from "./styles";

/** Groups shown before "Show all N". */
export const TOP_GROUPS = 8;

interface Group {
  key: string;
  /** Plain text of the label: tooltip and the bar's accessible name. */
  name: string;
  label: React.ReactNode;
  prs: number;
  avg_health: number | null;
  avg_findings: number | null;
}

const clamp = (n: number) => Math.min(100, Math.max(0, n));
const oneDecimal = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });

function Row({ group }: { group: Group }) {
  const t = useTranslations("leaderboardPanels.healthByGroup");
  const summary = (
    <span className="tnum" style={s.summary} title={t("summaryHint")}>
      {t("summary", { prs: group.prs, findings: oneDecimal.format(group.avg_findings ?? 0) })}
    </span>
  );
  const label = (
    <span style={s.label} title={group.name}>
      {group.label}
    </span>
  );
  if (group.avg_health === null) {
    return (
      <li style={s.row} data-series={group.key}>
        {label}
        <div style={s.track} />
        <span style={s.none}>{t("noHealth")}</span>
        {summary}
      </li>
    );
  }
  const health = Math.round(group.avg_health);
  return (
    <li style={s.row} data-series={group.key}>
      {label}
      <div style={s.track} role="img" aria-label={t("healthLabel", { name: group.name, health })}>
        <div style={{ ...s.fill, width: `${clamp(group.avg_health)}%`, background: healthColor(group.avg_health) }} />
      </div>
      <span className="tnum" style={s.health}>
        {health}
      </span>
      {summary}
    </li>
  );
}

interface HealthByGroupProps {
  title: string;
  subtitle: string;
  emptyTitle: string;
  emptyBody: string;
  groups: readonly Group[];
}

function HealthByGroup({ title, subtitle, emptyTitle, emptyBody, groups }: HealthByGroupProps) {
  const t = useTranslations("leaderboardPanels.healthByGroup");
  const [showAll, setShowAll] = React.useState(false);
  const canCollapse = groups.length > TOP_GROUPS;
  const visible = canCollapse && !showAll ? groups.slice(0, TOP_GROUPS) : groups;
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{title}</h3>
        <span style={s.subtitle}>{subtitle}</span>
      </div>
      {groups.length > 0 ? (
        <ul style={s.list} aria-label={title}>
          {visible.map((g) => (
            <Row key={g.key} group={g} />
          ))}
        </ul>
      ) : (
        <div style={s.empty}>
          <strong style={s.emptyTitle}>{emptyTitle}</strong>
          <span>{emptyBody}</span>
        </div>
      )}
      {canCollapse && (
        <div style={s.footer}>
          <button type="button" style={s.more} onClick={() => setShowAll(!showAll)}>
            {showAll ? t("showTop", { count: TOP_GROUPS }) : t("showAll", { count: groups.length })}
          </button>
        </div>
      )}
    </Card>
  );
}

export interface HealthByAuthorProps {
  items: readonly LeaderboardAuthorHealth[];
}

export function HealthByAuthor({ items }: HealthByAuthorProps) {
  const t = useTranslations("leaderboardPanels.healthByAuthor");
  const groups = items.map(
    (a): Group => ({
      ...a,
      key: a.author,
      name: a.author,
      label: (
        <>
          <Avatar name={a.author} size={16} />
          <span style={s.labelText}>{a.author}</span>
        </>
      ),
    }),
  );
  return <HealthByGroup title={t("title")} subtitle={t("subtitle")} emptyTitle={t("emptyTitle")} emptyBody={t("emptyBody")} groups={groups} />;
}

export interface HealthByRepoProps {
  items: readonly LeaderboardRepoHealth[];
  /** The response's repositories by id (GitLab host labels). */
  repos?: RepoLookup;
}

export function HealthByRepo({ items, repos = NO_REPOS }: HealthByRepoProps) {
  const t = useTranslations("leaderboardPanels.healthByRepo");
  const groups = items.map((r): Group => {
    const repo = repos.get(r.repo_id);
    // GitLab: the host follows the name in muted text and is clipped first, so the name stays readable.
    const host = repo?.provider === "gitlab" ? repo.host : undefined;
    return {
      ...r,
      key: r.repo_id,
      name: repoNameLabel(repos, r.repo_id, r.repo),
      label: (
        <span className="mono" style={s.repoLabel}>
          <span style={s.repoName}>{r.repo}</span>
          {host && <span style={s.repoHost}>· {host}</span>}
        </span>
      ),
    };
  });
  return <HealthByGroup title={t("title")} subtitle={t("subtitle")} emptyTitle={t("emptyTitle")} emptyBody={t("emptyBody")} groups={groups} />;
}
