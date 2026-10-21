import type { Metadata } from "next";
import { TourView } from "./_components/TourView";

export const metadata: Metadata = { title: "Onboarding Tour" };

type Props = { params: Promise<{ repoId: string }> };

/* Route: /repos/:repoId/onboarding — the repo's five-part onboarding tour. Thin entry: resolves the
   repo id; the screen lives in _components/TourView. Keyed by repo so a repo switch remounts the
   screen and drops any in-flight generation of the previous repo. */
export default async function OnboardingPage({ params }: Props) {
  const { repoId } = await params;
  return <TourView key={repoId} repoId={repoId} />;
}
