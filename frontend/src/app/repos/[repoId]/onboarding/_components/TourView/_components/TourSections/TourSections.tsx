/* TourSections — the five collapsible section cards in FR4 order. With no tour (loading or
   generating) each card shows skeleton lines instead of its content. */
"use client";

import { useTranslations } from "next-intl";
import { Skeleton } from "@devdigest/ui";
import type { OnboardingTour } from "@devdigest/shared";
import { ONBOARDING_SECTION_IDS, type OnboardingSectionId } from "@devdigest/shared/constants/onboarding";
import { SECTION_ICON } from "../../constants";
import { SectionCard } from "../SectionCard";
import { ArchitectureCard } from "../ArchitectureCard";
import { PathList } from "../PathList";
import { RunSteps } from "../RunSteps";
import { FirstTasks } from "../FirstTasks";
import { s } from "./styles";

function SectionContent({ id, tour, repoFullName }: { id: OnboardingSectionId; tour: OnboardingTour; repoFullName: string }) {
  switch (id) {
    case "architecture":
      return <ArchitectureCard architecture={tour.architecture} />;
    case "critical-paths":
      return <PathList items={tour.critical_paths} variant="critical" repoFullName={repoFullName} indexedSha={tour.indexed_sha} />;
    case "run-locally":
      return <RunSteps steps={tour.run_steps} />;
    case "reading-path":
      return <PathList items={tour.reading_path} variant="reading" repoFullName={repoFullName} indexedSha={tour.indexed_sha} />;
    case "first-tasks":
      return <FirstTasks tasks={tour.first_tasks} />;
  }
}

export function TourSections({ tour, repoFullName }: { tour: OnboardingTour | null; repoFullName: string }) {
  const t = useTranslations("onboarding");
  return (
    <div style={s.stack}>
      {ONBOARDING_SECTION_IDS.map((id) => (
        <SectionCard key={id} id={id} title={t(`sections.${id}`)} icon={SECTION_ICON[id]}>
          {tour ? (
            <SectionContent id={id} tour={tour} repoFullName={repoFullName} />
          ) : (
            <div aria-hidden style={s.skeleton}>
              <Skeleton height={14} />
              <Skeleton height={14} />
              <Skeleton height={14} />
            </div>
          )}
        </SectionCard>
      ))}
    </div>
  );
}
