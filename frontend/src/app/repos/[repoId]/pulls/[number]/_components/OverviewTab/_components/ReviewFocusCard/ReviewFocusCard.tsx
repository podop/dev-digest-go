/* ReviewFocusCard — "Review focus — read these first": a full-width card with the model's
   items in stored (reading) order and a count badge. An item opens Files changed at its file
   and line; a file outside the PR's diff gets an inline message instead. Hidden without a
   brief. Plain text only. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, Icon } from "@devdigest/ui";
import { usePrBrief } from "@/lib/hooks";
import type { OpenInDiff } from "../../useOpenInDiff";
import { s } from "./styles";

export interface ReviewFocusCardProps extends OpenInDiff {
  prId: string;
}

export function ReviewFocusCard({ prId, open, missingKey }: ReviewFocusCardProps) {
  const t = useTranslations("brief");
  const { data } = usePrBrief(prId);
  const items = data?.brief?.review_focus;
  if (!items) return null;

  return (
    <section style={s.card} aria-label={t("focus.title")}>
      <div style={s.header}>
        <Icon.ListChecks size={14} style={s.headerIcon} aria-hidden="true" />
        <h3 style={s.headerTitle}>{t("focus.title")}</h3>
        {items.length > 0 && <Badge color="var(--accent-text)" bg="var(--accent-bg)">{items.length}</Badge>}
      </div>
      {items.length === 0 ? (
        <p style={s.empty}>{t("focus.empty")}</p>
      ) : (
        <ol style={s.list}>
          {items.map((item, i) => {
            const ref = `${item.file}:${item.line}`;
            const key = `focus:${i}:${ref}`;
            return (
              <li key={key} style={s.item}>
                <span aria-hidden="true" style={s.bullet}>
                  ▸
                </span>
                <div style={s.body}>
                  <button
                    type="button"
                    className="mono"
                    style={s.ref}
                    title={ref}
                    onClick={() => open(key, { file: item.file, start_line: item.line, end_line: null })}
                  >
                    {ref}
                  </button>
                  <span style={s.reason}>{` — ${item.reason}`}</span>
                  {missingKey === key && <span style={s.notInDiff}>{t("notInDiff")}</span>}
                </div>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
