"use client";

import { useTranslations } from "next-intl";
import { SectionLabel } from "@devdigest/ui";
import { useGenerateBrief } from "@/lib/hooks/brief";
import { useLiveRunRefresh } from "@/lib/hooks/reviews";
import { BlastRadiusCard } from "./_components/BlastRadiusCard";
import { IntentCard } from "./_components/IntentCard";
import { PrBriefCard } from "./_components/PrBriefCard";
import { ReviewFocusCard } from "./_components/ReviewFocusCard";
import { RiskAreas } from "./_components/RiskAreas";
import { s } from "./styles";
import { useOpenInDiff } from "./useOpenInDiff";

interface OverviewTabProps {
  prId: string;
  repoId: string;
  /** PR number as in the route. */
  number: string;
  /** Paths of the PR's changed files. */
  changedPaths: readonly string[];
  prBody: string | null | undefined;
  repoFullName: string | null;
  headSha: string | null;
}

export function OverviewTab({ prId, repoId, number, changedPaths, prBody, repoFullName, headSha }: OverviewTabProps) {
  const t = useTranslations("prReview");
  // A review run that ends while Overview is open refreshes the brief banner's verdict and score.
  useLiveRunRefresh(prId);
  // The banner, the risk areas and the review focus read one brief; the generate POST (one,
  // however many cards show its state) and the Files changed links are shared from here.
  const generate = useGenerateBrief(prId);
  const diffLinks = useOpenInDiff({ repoId, number, changedPaths });
  return (
    <div style={s.stack}>
      <PrBriefCard
        prId={prId}
        headSha={headSha}
        generating={generate.isPending}
        generateError={generate.error}
        onGenerate={() => generate.mutate()}
      />
      <div style={s.cards}>
        <IntentCard prId={prId} repoFullName={repoFullName} headSha={headSha}>
          <RiskAreas prId={prId} {...diffLinks} />
        </IntentCard>
        <BlastRadiusCard prId={prId} repoId={repoId} repoFullName={repoFullName} headSha={headSha} />
      </div>
      <ReviewFocusCard prId={prId} {...diffLinks} />
      {prBody && (
        <section>
          <SectionLabel icon="MessageSquare">{t("overview.description")}</SectionLabel>
          <div style={s.descriptionBox}>{prBody}</div>
        </section>
      )}
    </div>
  );
}
