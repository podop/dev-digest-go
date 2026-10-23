/* HealthBySize — the leaderboard's "Health by PR size" card (FR22, mockup): one row per size bucket
   with a bar coloured by average health (the 85 / 60 bands of the table's health rings), the number and
   "N PRs · X findings"; an empty bucket shows "—" and an empty track. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardSizeBucket } from "@devdigest/shared";
import { Card } from "@devdigest/ui";
import { healthColor } from "../../helpers";
import { s } from "./styles";

export interface HealthBySizeProps {
  buckets: readonly LeaderboardSizeBucket[];
}

const clamp = (n: number) => Math.min(100, Math.max(0, n));
const oneDecimal = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });

function Row({ bucket }: { bucket: LeaderboardSizeBucket }) {
  const t = useTranslations("leaderboardPanels.healthBySize");
  const size = t(`size.${bucket.size}`);
  if (bucket.prs === 0 || bucket.avg_health === null) {
    return (
      <li style={s.row} data-series={bucket.size}>
        <span style={s.size}>{size}</span>
        <div style={s.track} />
        <span />
        <span style={s.none}>{t("emptyBucket")}</span>
      </li>
    );
  }
  const health = Math.round(bucket.avg_health);
  return (
    <li style={s.row} data-series={bucket.size}>
      <span style={s.size}>{size}</span>
      <div style={s.track} role="img" aria-label={t("healthLabel", { size, health })}>
        <div style={{ ...s.fill, width: `${clamp(bucket.avg_health)}%`, background: healthColor(bucket.avg_health) }} />
      </div>
      <span className="tnum" style={s.health}>
        {health}
      </span>
      <span className="tnum" style={s.summary} title={t("summaryHint")}>
        {t("summary", { prs: bucket.prs, findings: oneDecimal.format(bucket.avg_findings ?? 0) })}
      </span>
    </li>
  );
}

export function HealthBySize({ buckets }: HealthBySizeProps) {
  const t = useTranslations("leaderboardPanels.healthBySize");
  const hasPrs = buckets.some((b) => b.prs > 0);
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{t("title")}</h3>
        <span style={s.subtitle}>{t("subtitle")}</span>
      </div>
      {hasPrs ? (
        <ul style={s.list} aria-label={t("title")}>
          {buckets.map((b) => (
            <Row key={b.size} bucket={b} />
          ))}
        </ul>
      ) : (
        <div style={s.empty}>
          <strong style={s.emptyTitle}>{t("emptyTitle")}</strong>
          <span>{t("emptyBody")}</span>
        </div>
      )}
    </Card>
  );
}
