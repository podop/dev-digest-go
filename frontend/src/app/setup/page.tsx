import type { Metadata } from "next";
import { redirect } from "next/navigation";

export const metadata: Metadata = { title: "Setup" };

type Props = { searchParams: Promise<{ step?: string | string[] }> };

/* Route: /setup?step=… — kept for old links and bookmarks. First-run setup now runs inline in
   Settings › First-run setup, so this is a server redirect that keeps the step query; the setup
   flow there resolves the step (a missing or invalid one opens the first incomplete step). */
export default async function SetupPage({ searchParams }: Props) {
  const { step } = await searchParams;
  const requested = Array.isArray(step) ? step[0] : step;
  redirect(requested ? `/settings/setup?${new URLSearchParams({ step: requested })}` : "/settings/setup");
}
