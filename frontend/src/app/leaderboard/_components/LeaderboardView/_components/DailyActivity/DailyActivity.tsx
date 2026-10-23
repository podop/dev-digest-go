/* DailyActivity — the leaderboard's "Daily activity" card (FR22, mockup): per day two grouped bars
   (opened = blue, merged = green) scaled to the busiest value, the counts above the bars, a red
   circle with the day's critical count below, the day label ("Mon 28") and a legend. Colour is never
   the only carrier: counts are text, and each day has an accessible sentence. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardDay } from "@devdigest/shared";
import { Card } from "@devdigest/ui";
import { formatDay } from "./helpers";
import { BAR_AREA, s } from "./styles";

export interface DailyActivityProps {
  days: readonly LeaderboardDay[];
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

function Day({ day, max }: { day: LeaderboardDay; max: number }) {
  const t = useTranslations("leaderboardPanels.dailyActivity");
  const label = formatDay(day.date);
  return (
    <div
      data-series={day.date}
      role="img"
      aria-label={t("dayLabel", { day: label, opened: day.opened, merged: day.merged, critical: day.critical })}
      style={s.column}
    >
      <div style={s.bars}>
        <Bar value={day.opened} max={max} color={OPENED} kind="opened" />
        <Bar value={day.merged} max={max} color={MERGED} kind="merged" />
      </div>
      <div style={s.criticalSlot} title={day.critical > 0 ? t("criticalHint") : undefined}>
        {day.critical > 0 && (
          <span className="tnum" style={s.critical}>
            {day.critical}
          </span>
        )}
      </div>
      <span style={s.tick}>{label}</span>
    </div>
  );
}

export function DailyActivity({ days }: DailyActivityProps) {
  const t = useTranslations("leaderboardPanels.dailyActivity");
  const max = Math.max(0, ...days.map((d) => Math.max(d.opened, d.merged)));
  const active = days.some((d) => d.opened > 0 || d.merged > 0 || d.critical > 0);
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{t("title")}</h3>
        <span style={s.subtitle}>{t("subtitle")}</span>
      </div>
      {active ? (
        <>
          <div style={s.scroll}>
            <div style={s.chart}>
              {days.map((d) => (
                <Day key={d.date} day={d} max={max} />
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
