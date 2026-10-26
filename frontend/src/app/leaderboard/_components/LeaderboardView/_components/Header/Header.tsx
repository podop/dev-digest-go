/* Header — title, window label, repo select, period toggle and the per-repo sync note of the
   PR Leaderboard (FR18, FR23). Controlled by the view, which owns the URL: it renders what it is
   given and reports the user's choice; no data fetching here. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { LEADERBOARD_PERIODS, type LeaderboardPeriod, type LeaderboardPeriodKey, type LeaderboardRepo } from "@devdigest/shared";
import { repoLabel, type CodeHostRepo } from "@/lib/code-host";
import { Select } from "@/components/select";
import { ALL_REPOS } from "../../constants";
import { syncedAgo, windowRanges } from "../../helpers";
import { s } from "./styles";

export interface HeaderProps {
  period: LeaderboardPeriodKey;
  /** The repo filter in effect: "all" or an id from `repos`. */
  repo: string;
  /** The repositories to offer besides "All"; undefined while the list loads (the select is disabled). */
  repos: readonly (CodeHostRepo & { id: string; full_name: string })[] | undefined;
  /** The resolved window of the loaded response; null while loading or failed (no label, no digits). */
  window: LeaderboardPeriod | null;
  /** The response carries a previous summary: only then the label says what the week is compared with (FR25). */
  compared: boolean;
  /** The repositories in the filter with their last-polled time; null until the response is loaded. */
  syncRepos: readonly LeaderboardRepo[] | null;
  onPeriodChange: (next: LeaderboardPeriodKey) => void;
  onRepoChange: (next: string) => void;
}

type Translate = ReturnType<typeof useTranslations<"leaderboard">>;

/** "Week of Sep 28 – Oct 4 · compared with Sep 21 – 27"; without a previous summary (30d, or a previous week without rows) just the range (FR18, FR25). */
function windowLabel(t: Translate, isWeek: boolean, ranges: { range: string; previous: string | null }): string {
  if (ranges.previous === null) return t(isWeek ? "header.window.weekAlone" : "header.window.rangeAlone", { range: ranges.range });
  return t(isWeek ? "header.window.week" : "header.window.range", { range: ranges.range, previous: ranges.previous });
}

export function Header({ period, repo, repos, window, compared, syncRepos, onPeriodChange, onRepoChange }: HeaderProps) {
  const t = useTranslations("leaderboard");
  const ranges = window ? windowRanges(window, compared) : null;
  const label = ranges ? windowLabel(t, window?.key === "week", ranges) : null;

  const options = React.useMemo(
    () => [
      { value: ALL_REPOS, label: t("header.repo.all") },
      ...(repos ?? []).map((r) => ({ value: r.id, label: repoLabel(r) })),
    ],
    [repos, t],
  );

  return (
    <div style={s.wrap}>
      <div style={s.top}>
        <div style={s.text}>
          <h1 style={s.h1}>{t("title")}</h1>
          <p style={s.windowLabel} aria-live="polite">
            {label}
          </p>
        </div>
        <div style={s.controls}>
          <span style={s.select}>
            <Select
              value={repo}
              options={options}
              mono={false}
              chevron="down"
              aria-label={t("header.repo.label")}
              disabled={repos === undefined}
              onChange={onRepoChange}
            />
          </span>
          <fieldset aria-label={t("header.period.label")} style={s.toggle}>
            {LEADERBOARD_PERIODS.map((p) => (
              <button
                key={p}
                type="button"
                aria-pressed={p === period}
                style={s.toggleButton(p === period)}
                onClick={() => p !== period && onPeriodChange(p)}
              >
                {t(`header.period.${p}`)}
              </button>
            ))}
          </fieldset>
        </div>
      </div>
      {syncRepos && syncRepos.length > 0 && <SyncNote repos={syncRepos} />}
    </div>
  );
}

function SyncNote({ repos }: { repos: readonly LeaderboardRepo[] }) {
  const t = useTranslations("leaderboard.header.sync");
  return (
    <ul aria-label={t("label")} style={s.sync}>
      {repos.map((r) => {
        const ago = syncedAgo(r.last_polled_at);
        return (
          <li key={r.id}>
            <span style={s.syncRepo}>{repoLabel(r)}</span> {ago === null ? t("never") : t("synced", { when: ago })}
          </li>
        );
      })}
    </ul>
  );
}
