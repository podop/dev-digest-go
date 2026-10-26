/* NotConnectedBanner — FR49: a GitLab repository without a connection for its host shows this on its PR list and
   PR detail. Stored PRs, reviews and findings stay usable; the banner points to the GitLab section of Settings. */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { s } from "./styles";

const GITLAB_SETTINGS_HREF = "/settings/gitlab";

export function NotConnectedBanner({ host }: { host: string }) {
  const t = useTranslations("prReview");
  return (
    <div role="note" style={s.root}>
      <Icon.AlertTriangle size={14} style={s.icon} />
      <span>{t("notConnected.body", { host })}</span>
      <Link href={GITLAB_SETTINGS_HREF} style={s.link}>
        {t("notConnected.link")}
      </Link>
    </div>
  );
}
