/* BriefBanner — the PR Brief's head: worst verdict, findings/blocker counts, score of the
   current reviews, the model-written summary (plain text), the refresh action and the
   brief's own cost. With no review only the summary (and cost) show. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, Button, CircularScore, Icon } from "@devdigest/ui";
import { CostText } from "@/components/cost-text";
import { formatTokens } from "@/lib/format-usage";
import { VERDICT_META } from "@/app/repos/[repoId]/pulls/[number]/_components/VerdictBanner";
import type { BannerStats } from "../../helpers";
import { s } from "./styles";

export interface BriefBannerProps {
  summary: string;
  stats: BannerStats;
  tokensIn: number;
  tokensOut: number;
  costUsd: number | null;
  /** Emphasises the refresh button (the card shows no banner while a generation runs). */
  stale: boolean;
  onRefresh: () => void;
}

export function BriefBanner({ summary, stats, tokensIn, tokensOut, costUsd, stale, onRefresh }: BriefBannerProps) {
  const t = useTranslations("brief");
  const meta = stats.verdict ? VERDICT_META[stats.verdict] : null;
  const VIcon = meta ? Icon[meta.icon] : null;
  return (
    <div style={s.wrap}>
      {meta && VIcon && (
        <div style={s.iconBox(meta.bg, meta.c)}>
          <VIcon size={22} />
        </div>
      )}
      <div style={s.main}>
        <div style={s.titleRow}>
          {meta && <span style={s.label(meta.c)}>{t(`banner.verdict.${meta.labelKey}`)}</span>}
          {stats.hasReviews && (
            <Badge color="var(--text-secondary)">
              {t("banner.findingsCount", { count: stats.findings })}
              {stats.blockers > 0 ? t("banner.blockers", { count: stats.blockers }) : ""}
            </Badge>
          )}
          <Button
            kind={stale ? "primary" : "tertiary"}
            size="sm"
            icon="RefreshCw"
            onClick={onRefresh}
            style={s.refresh}
            aria-label={t("refresh")}
            title={t("refresh")}
          />
        </div>
        <p style={s.summary}>{summary}</p>
      </div>
      <div style={s.scoreCol}>
        {stats.score != null && (
          <>
            <CircularScore score={stats.score} size={52} stroke={5} />
            <span style={s.scoreLabel}>{t("banner.prScore")}</span>
          </>
        )}
        <div style={s.costRow} title={t("banner.cost")}>
          <Icon.DollarSign size={11} style={s.costIcon} />
          <CostText usd={costUsd} />
          <span className="mono" style={s.costTokens}>
            {formatTokens(tokensIn, tokensOut)}
          </span>
        </div>
      </div>
    </div>
  );
}
