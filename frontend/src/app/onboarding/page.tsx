import type { Metadata } from "next";
import { redirect } from "next/navigation";

export const metadata: Metadata = { title: "Add repository" };

/* Route: /onboarding — kept for old links and bookmarks. A server redirect (no client
   flash) to the repository step of Settings › First-run setup, which applies the step rules and moves to an
   earlier step when a prerequisite (model key, GitHub token) is missing (FR28). */
export default function OnboardingPage() {
  redirect("/settings/setup?step=repo");
}
