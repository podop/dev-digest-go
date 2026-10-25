import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { SettingsView } from "./_components/SettingsView";
import { DEFAULT_SECTION, isSettingsSection, sectionHref } from "./_components/SettingsView/constants";

export const metadata: Metadata = { title: "Settings" };

type Props = { params: Promise<{ section: string }> };

/* Route: /settings/:section. Thin route entry — an unknown key replaces the URL with the
   default section; the view, its section panels, styles, constants and i18n are colocated
   under _components/SettingsView. */
export default async function SettingsPage({ params }: Props) {
  const { section } = await params;
  if (!isSettingsSection(section)) redirect(sectionHref(DEFAULT_SECTION));
  return <SettingsView section={section} />;
}
