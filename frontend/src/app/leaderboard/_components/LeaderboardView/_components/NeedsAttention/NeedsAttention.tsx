/* NeedsAttention — the leaderboard's "Needs attention" card (FR22, mockup): a count, then one row per
   PR with mono repo#number (a link to the PR detail page), a truncated bold title, the author and outlined reason badges (red for the critical
   reasons, neutral for the rest). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import type { LeaderboardAttention, LeaderboardAttentionReason, LeaderboardNeedsAttention } from "@devdigest/shared";
import { Avatar, Badge, Card } from "@devdigest/ui";
import { prDetailPath } from "@/lib/pr-urls";
import { s } from "./styles";

export interface NeedsAttentionProps {
  data: LeaderboardNeedsAttention;
}

type Translate = ReturnType<typeof useTranslations<"needsAttention">>;

function reasonLabel(t: Translate, r: LeaderboardAttentionReason): string {
  switch (r.kind) {
    case "merged_open_critical":
      return t("reason.merged_open_critical", { count: r.count ?? 0 });
    case "open_critical":
      return t("reason.open_critical", { count: r.count ?? 0 });
    case "open_long":
      return t("reason.open_long", { days: r.days ?? 0 });
    case "no_review":
      return t("reason.no_review");
    case "new_commits":
      return t("reason.new_commits");
  }
}

/** The reasons that mean an open critical finding (shown in red). */
const isCriticalReason = (r: LeaderboardAttentionReason): boolean => r.kind === "merged_open_critical" || r.kind === "open_critical";

function Item({ item, t }: { item: LeaderboardAttention; t: Translate }) {
  return (
    <li style={s.item} data-series={item.pr_id}>
      <div style={s.head}>
        <Link href={prDetailPath(encodeURIComponent(item.repo_id), item.number)} className="mono" style={s.ref}>
          {item.repo}#{item.number}
        </Link>
        <span style={s.prTitle} title={item.title}>
          {item.title}
        </span>
        <span style={s.author}>
          <Avatar name={item.author} size={20} />
          {item.author}
        </span>
      </div>
      <div style={s.reasons}>
        {item.reasons.map((r) => (
          <Badge key={r.kind} style={isCriticalReason(r) ? s.reasonCritical : s.reason}>
            {reasonLabel(t, r)}
          </Badge>
        ))}
      </div>
    </li>
  );
}

export function NeedsAttention({ data }: NeedsAttentionProps) {
  const t = useTranslations("leaderboardPanels.needsAttention");
  const { total, items } = data;
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{t("title")}</h3>
        <span style={s.count}>{total}</span>
        {total > items.length && items.length > 0 && <span style={s.shown}>{t("shown", { shown: items.length, total })}</span>}
      </div>
      {items.length === 0 ? (
        <div style={s.empty}>
          <strong style={s.emptyTitle}>{t("emptyTitle")}</strong>
        </div>
      ) : (
        <ul style={s.list} aria-label={t("title")}>
          {items.map((item) => (
            <Item key={item.pr_id} item={item} t={t} />
          ))}
        </ul>
      )}
    </Card>
  );
}
