/* TourHeader — "Onboarding for <repo>", the generated-from line (or the generating status) and the
   Regenerate / Share link buttons. */
"use client";

import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { s } from "./styles";

export function TourHeader({
  repoName,
  meta,
  generating,
  showActions,
  onRegenerate,
  onShare,
}: {
  repoName: string;
  /** Pre-rendered "Generated from index of N files · last refreshed …" line, when a tour exists. */
  meta: string | null;
  generating: boolean;
  /** Regenerate / Share are shown once a tour exists or one is being generated. */
  showActions: boolean;
  onRegenerate: () => void;
  onShare: () => void;
}) {
  const t = useTranslations("onboarding");
  return (
    <header style={s.header}>
      <div style={s.text}>
        <h1 style={s.h1}>
          {t.rich("heading", {
            name: repoName,
            repo: (chunks) => (
              <span className="mono" style={s.repo}>
                {chunks}
              </span>
            ),
          })}
        </h1>
        {(generating || meta) && (
          <p aria-live="polite" style={s.meta}>
            {generating ? t("generate.status") : meta}
          </p>
        )}
      </div>
      {showActions && (
        <div style={s.actions}>
          <Button icon="RefreshCw" disabled={generating} onClick={onRegenerate}>
            {generating ? t("regenerating") : t("regenerate")}
          </Button>
          <Button icon="Link" kind="ghost" disabled={generating} onClick={onShare}>
            {t("share")}
          </Button>
        </div>
      )}
    </header>
  );
}
