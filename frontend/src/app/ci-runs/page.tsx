import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";
import { CiRunsView } from "./_components/CiRunsView";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations("ci");
  return { title: t("page.metaTitle") };
}

/* Route: /ci-runs — CI Runs: agent reviews that ran inside GitHub Actions, pulled in by Refresh.
   Thin entry: the screen lives in _components/CiRunsView. */
export default function CiRunsPage() {
  return <CiRunsView />;
}
