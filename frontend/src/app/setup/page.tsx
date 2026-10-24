import type { Metadata } from "next";
import { SetupView } from "./_components/SetupView";

export const metadata: Metadata = { title: "Setup" };

type Props = { searchParams: Promise<{ step?: string | string[] }> };

/* Route: /setup?step=model|github|repo (first-run setup). Thin entry — the view, its steps,
   styles, constants and i18n live under _components/SetupView. */
export default async function SetupPage({ searchParams }: Props) {
  const { step } = await searchParams;
  return <SetupView requestedStep={Array.isArray(step) ? step[0] : step} />;
}
