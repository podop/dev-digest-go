/* PathList — body of the "Critical paths" and "Guided reading path" sections. Paths are monospace,
   truncated with the full path as tooltip. Critical paths get an "Open" button (GitHub at the
   tour's indexed commit, new tab); the reading path is a numbered list. Empty → "Not enough information". */
"use client";

import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import type { OnboardingPathItem } from "@devdigest/shared";
import { openPathOnGithub } from "./helpers";
import { s } from "./styles";

export type PathListVariant = "critical" | "reading";

export function PathList({
  items,
  variant,
  repoFullName,
  indexedSha,
}: {
  items: readonly OnboardingPathItem[];
  variant: PathListVariant;
  /** `owner/name`, for the Open link. */
  repoFullName: string;
  /** Commit the tour was generated at; Open is pinned to it. */
  indexedSha: string;
}) {
  const t = useTranslations("onboarding");
  if (items.length === 0) return <p style={s.empty}>{t("empty")}</p>;

  if (variant === "reading") {
    return (
      <ol aria-label={t("paths.readingList")} style={s.readingList}>
        {items.map((item, i) => (
          <li key={item.path} style={s.readingRow}>
            <span aria-hidden style={s.num}>
              {i + 1}
            </span>
            <div style={s.readingText}>
              <span className="mono" title={item.path} style={s.path}>
                {item.path}
              </span>
              <span style={s.reasonBelow}>{item.reason}</span>
            </div>
          </li>
        ))}
      </ol>
    );
  }

  return (
    <ul aria-label={t("paths.criticalList")} style={s.criticalList}>
      {items.map((item) => (
        <li key={item.path} style={s.criticalRow}>
          <Icon.FileText size={14} style={s.icon} />
          <span className="mono" title={item.path} style={{ ...s.path, ...s.pathInline }}>
            {item.path}
          </span>
          <span style={s.reasonInline}>— {item.reason}</span>
          <Button
            size="sm"
            aria-label={t("paths.openAria", { path: item.path })}
            onClick={() => openPathOnGithub(repoFullName, indexedSha, item.path)}
          >
            {t("paths.open")}
          </Button>
        </li>
      ))}
    </ul>
  );
}
