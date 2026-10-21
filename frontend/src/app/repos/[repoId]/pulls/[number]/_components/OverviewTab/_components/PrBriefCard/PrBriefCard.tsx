/* PrBriefCard — the top of Overview (specs/2026-10-01-pr-brief.md §9): a "PR Brief" label and
   under it skeleton while loading/generating, an explanation + Generate button before the first
   brief, and with a brief the banner (verdict, score, summary, refresh) plus notes for missing
   inputs / stale / failed generation. Risk areas and Review focus live in their own cards.
   The generate mutation is owned by OverviewTab (three cards read one brief), so this card
   gets its state as props. All model-written text is rendered as plain text. */
"use client";

import type { ReactNode } from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, SectionLabel, Skeleton } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { usePrBrief, usePrReviews } from "@/lib/hooks";
import { BriefBanner } from "./_components/BriefBanner";
import { SKELETON_HEIGHT } from "./constants";
import { bannerStats, isBriefStale } from "./helpers";
import { s } from "./styles";

export interface PrBriefCardProps {
  prId: string;
  /** Head SHA of the PR as the page shows it. */
  headSha: string | null;
  /** A generate/refresh POST is in flight. */
  generating: boolean;
  /** Error of the last generate POST (null when none / since cleared). */
  generateError: unknown;
  onGenerate: () => void;
}

export function PrBriefCard({ prId, headSha, generating, generateError, onGenerate }: PrBriefCardProps) {
  const t = useTranslations("brief");
  const { data, isLoading, isError, error, refetch } = usePrBrief(prId);
  const { data: reviews } = usePrReviews(prId);

  if (isLoading) {
    return (
      <BriefSection busy={false}>
        <Skeleton height={SKELETON_HEIGHT} />
      </BriefSection>
    );
  }

  if (isError) {
    return (
      <BriefSection busy={false}>
        <ErrorState
          title={t("error.title")}
          body={error instanceof ApiError ? error.message : t("error.body")}
          onRetry={() => refetch()}
          retryLabel={t("retry")}
        />
      </BriefSection>
    );
  }

  const brief = data?.brief ?? null;
  const failure =
    generateError == null
      ? null
      : generateError instanceof ApiError
        ? t("error.generateFailed", { message: generateError.message })
        : t("error.generateFailedGeneric");
  const errorNote = failure && (
    <p role="alert" style={s.errorNote}>
      {failure}
    </p>
  );

  if (generating) {
    return (
      <BriefSection busy>
        <div style={s.card}>
          <p style={s.explanation}>{t("generating")}</p>
          <Skeleton height={SKELETON_HEIGHT} />
          <div>
            <Button kind="primary" icon="Sparkles" disabled loading>
              {brief ? t("refresh") : t("empty.cta")}
            </Button>
          </div>
        </div>
      </BriefSection>
    );
  }

  if (!brief) {
    return (
      <BriefSection busy={false}>
        <div style={s.card}>
          <h3 style={s.emptyTitle}>{t("unavailable")}</h3>
          <p style={s.explanation}>{t("empty.body")}</p>
          {errorNote}
          <div>
            <Button kind="primary" icon="Sparkles" onClick={onGenerate}>
              {t("empty.cta")}
            </Button>
          </div>
        </div>
      </BriefSection>
    );
  }

  const stale = isBriefStale(data?.stale ?? false, brief.head_sha, headSha);

  return (
    <BriefSection busy={false}>
      <BriefBanner
        summary={brief.summary}
        stats={bannerStats(reviews)}
        tokensIn={brief.tokens_in}
        tokensOut={brief.tokens_out}
        costUsd={brief.cost_usd}
        stale={stale}
        onRefresh={onGenerate}
      />
      {(stale || errorNote || brief.missing_inputs.length > 0) && (
        <div style={s.notes}>
          {stale && <p style={s.staleNote}>{t("staleHint")}</p>}
          {errorNote}
          {brief.missing_inputs.map((m) => {
            const params = { input: t(`missing.input.${m.input}`), reason: t(`missing.reason.${m.reason}`), detail: m.detail ?? "" };
            return (
              <p key={`${m.input}/${m.reason}`} style={s.note}>
                {m.detail ? t("missing.itemDetail", params) : t("missing.item", params)}
              </p>
            );
          })}
        </div>
      )}
    </BriefSection>
  );
}

/** The "PR BRIEF" label over whatever the brief area shows. */
function BriefSection({ busy, children }: { busy: boolean; children: ReactNode }) {
  const t = useTranslations("brief");
  return (
    <section aria-label={t("title")} aria-busy={busy || undefined}>
      <SectionLabel icon="FileText">{t("title")}</SectionLabel>
      <div style={s.body}>{children}</div>
    </section>
  );
}
