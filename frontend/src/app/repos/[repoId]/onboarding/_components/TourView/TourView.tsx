/* TourView — /repos/:repoId/onboarding: header (name, generated-from line, Regenerate, Share link),
   "On this page" TOC and the five section cards, plus the empty / generating / stale / error
   states. The page keys it by repo, so switching repos remounts it: a generation started for
   repo A reports (toast, cache) only to A and never renders on B. */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, ErrorState } from "@devdigest/ui";
import { ONBOARDING_SECTION_IDS } from "@devdigest/shared/constants/onboarding";
import { AppShell } from "@/components/app-shell";
import { RepoNotFound } from "@/components/repo-not-found";
import { useGenerateOnboardingTour, useOnboardingTour, useRepoIntelStatus } from "@/lib/hooks";
import { useActiveRepo, useRepoNotFound } from "@/lib/repo-context";
import { copyText } from "@/lib/clipboard";
import { useToast } from "@/lib/toast";
import { PROVIDER_NOT_CONFIGURED, SETTINGS_MODELS_HREF } from "./constants";
import { describeGenerateError, readyTour, relativeTime, shareUrl } from "./helpers";
import { useActiveSection } from "./useActiveSection";
import { EmptyTour } from "./_components/EmptyTour";
import { Notice } from "./_components/Notice";
import { TourHeader } from "./_components/TourHeader";
import { TourSections } from "./_components/TourSections";
import { TourToc } from "./_components/TourToc";
import { s } from "./styles";

export function TourView({ repoId }: { repoId: string }) {
  const t = useTranslations("onboarding");
  const tc = useTranslations("common");
  const toast = useToast();
  const { repos } = useActiveRepo();
  const repoNotFound = useRepoNotFound(repoId);
  const repo = repos.find((r) => r.id === repoId);
  const tourQuery = useOnboardingTour(repoId);
  const intel = useRepoIntelStatus(repoId);
  const generate = useGenerateOnboardingTour(repoId);

  const generating = generate.isPending;
  const ready = readyTour(tourQuery.data);
  const indexNotReady = intel.data?.filesIndexed === 0;
  const showCards = tourQuery.isLoading || generating || ready !== null;
  const { active, goTo } = useActiveSection(ONBOARDING_SECTION_IDS, ready !== null && !generating);
  const providerMissing = generate.isError && describeGenerateError(generate.error).code === PROVIDER_NOT_CONFIGURED;

  const onGenerate = () =>
    generate.mutate(undefined, {
      onSuccess: () => toast.success(t("updated")),
      onError: (err) => {
        const { messageKey, code } = describeGenerateError(err);
        const message = t(`errors.${messageKey}`);
        toast.error(code ? t("errors.withCode", { message, code }) : message);
      },
    });

  const onShare = async () => {
    const url = shareUrl(window.location.origin, window.location.pathname, active);
    if (await copyText(url)) toast.success(t("shareCopied"));
    else toast.error(t("shareFailed"));
  };

  const crumb = [{ label: repo?.full_name ?? repoId, mono: true }, { label: t("title") }];

  if (repoNotFound) {
    return (
      <AppShell crumb={crumb}>
        <RepoNotFound />
      </AppShell>
    );
  }

  const staleBody = ready?.stale_reason === "index_changed" ? t("stale.indexChanged") : ready?.stale_reason === "prompt_changed" ? t("stale.promptChanged") : t("stale.body");

  return (
    <AppShell crumb={crumb}>
      <div style={s.page}>
        {showCards && <TourToc ids={ONBOARDING_SECTION_IDS} active={active} onSelect={goTo} />}
        <div style={s.main}>
          <TourHeader
            repoName={repo?.name ?? repoId}
            meta={
              ready && !generating
                ? t("meta", { count: ready.tour.files_indexed, when: relativeTime(ready.tour.generated_at) })
                : null
            }
            generating={generating}
            showActions={ready !== null || generating}
            onRegenerate={onGenerate}
            onShare={() => void onShare()}
          />

          {providerMissing && (
            <Notice
              tone="crit"
              label={t("errors.providerNotConfigured")}
              action={
                <Link href={SETTINGS_MODELS_HREF} style={s.settingsLink}>
                  {t("errors.providerSettings")}
                </Link>
              }
            >
              {t("errors.providerNotConfigured")}
            </Notice>
          )}

          {ready?.stale && !generating && (
            <Notice
              tone="warn"
              label={t("stale.title")}
              action={
                <Button size="sm" onClick={onGenerate}>
                  {t("regenerate")}
                </Button>
              }
            >
              <span style={s.noticeTitle}>{t("stale.title")}</span>
              {staleBody}
            </Notice>
          )}

          {tourQuery.isError && !ready && !generating && (
            <ErrorState
              title={t("loadError.title")}
              body={tc("states.error")}
              onRetry={() => void tourQuery.refetch()}
              retryLabel={tc("actions.retry")}
            />
          )}

          {showCards ? (
            <TourSections tour={generating ? null : (ready?.tour ?? null)} repoFullName={repo?.full_name ?? ""} />
          ) : (
            !tourQuery.isError && <EmptyTour indexNotReady={indexNotReady} onGenerate={onGenerate} />
          )}
        </div>
      </div>
    </AppShell>
  );
}
