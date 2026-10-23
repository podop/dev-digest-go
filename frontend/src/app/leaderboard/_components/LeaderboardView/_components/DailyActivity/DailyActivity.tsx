/* DailyActivity — the leaderboard's activity card (FR22, D22, mockup): per point two grouped bars
   (opened = blue, merged = green) scaled to the busiest value, the counts above the bars, a red
   circle with the point's critical count below, its label and a legend. A point is a day ("Mon 28")
   for `week` / `last_week` and a week ("Wk of Oct 5") for `30d`; the title follows. Colour is never
   the only carrier: counts are text, and each point has an accessible sentence. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardActivity, LeaderboardActivityPoint } from "@devdigest/shared";
import { Card } from "@devdigest/ui";
import { formatDay, formatMonthDay } from "./helpers";
import { BAR_AREA, s } from "./styles";

export interface DailyActivityProps {
  activity: LeaderboardActivity;
}

const OPENED = "var(--accent)";
const MERGED = "var(--ok)";

function Bar({ value, max, color, kind }: { value: number; max: number; color: string; kind: "opened" | "merged" }) {
  return (
    <div style={s.barWrap}>
      {value > 0 && (
        <span className="tnum" style={s.count}>
          {value}
        </span>
      )}
      <div data-bar={kind} style={{ ...s.bar, height: max > 0 ? (value / max) * BAR_AREA : 0, background: color }} />
    </div>
  );
}

function Point({ point, granularity, max }: { point: LeaderboardActivityPoint; granularity: LeaderboardActivity["granularity"]; max: number }) {
  const t = useTranslations("leaderboardPanels.dailyActivity");
  const label = granularity === "week" ? t("weekLabel", { date: formatMonthDay(point.from) }) : formatDay(point.from);
  return (
    <div
      data-series={point.from}
      role="img"
      aria-label={t("pointLabel", { label, opened: point.opened, merged: point.merged, critical: point.critical })}
      style={s.column}
    >
      <div style={s.bars}>
        <Bar value={point.opened} max={max} color={OPENED} kind="opened" />
        <Bar value={point.merged} max={max} color={MERGED} kind="merged" />
      </div>
      <div style={s.criticalSlot} title={point.critical > 0 ? t("criticalHint") : undefined}>
        {point.critical > 0 && (
          <span className="tnum" style={s.critical}>
            {point.critical}
          </span>
        )}
      </div>
      <span style={s.tick}>{label}</span>
    </div>
  );
}

export function DailyActivity({ activity }: DailyActivityProps) {
  const t = useTranslations("leaderboardPanels.dailyActivity");
  const { granularity, points } = activity;
  const max = Math.max(0, ...points.map((p) => Math.max(p.opened, p.merged)));
  const active = points.some((p) => p.opened > 0 || p.merged > 0 || p.critical > 0);
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{t(granularity === "week" ? "weeklyTitle" : "title")}</h3>
        <span style={s.subtitle}>{t(granularity === "week" ? "weeklySubtitle" : "subtitle")}</span>
      </div>
      {active ? (
        <>
          <div style={s.scroll}>
            <div style={s.chart}>
              {points.map((p) => (
                <Point key={p.from} point={p} granularity={granularity} max={max} />
              ))}
            </div>
          </div>
          <div style={s.legend}>
            <span style={s.legendItem}>
              <span aria-hidden style={{ ...s.swatch, background: OPENED }} />
              {t("opened")}
            </span>
            <span style={s.legendItem}>
              <span aria-hidden style={{ ...s.swatch, background: MERGED }} />
              {t("merged")}
            </span>
            <span style={s.legendItem}>
              <span aria-hidden style={s.dot} />
              {t("critical")}
            </span>
          </div>
        </>
      ) : (
        <div style={s.empty}>
          <strong style={s.emptyTitle}>{t("emptyTitle")}</strong>
          <span>{t("emptyBody")}</span>
        </div>
      )}
    </Card>
  );
}
