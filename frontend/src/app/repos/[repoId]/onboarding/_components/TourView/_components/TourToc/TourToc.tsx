/* TourToc — "On this page": one button per section; the active one carries aria-current. */
"use client";

import { useTranslations } from "next-intl";
import type { OnboardingSectionId } from "@devdigest/shared/constants/onboarding";
import { s } from "./styles";

export function TourToc({
  ids,
  active,
  onSelect,
}: {
  ids: readonly OnboardingSectionId[];
  active: OnboardingSectionId;
  onSelect: (id: OnboardingSectionId) => void;
}) {
  const t = useTranslations("onboarding");
  return (
    <nav aria-label={t("toc")} style={s.nav}>
      <div aria-hidden style={s.label}>
        {t("toc")}
      </div>
      <ul style={s.list}>
        {ids.map((id) => {
          const on = id === active;
          return (
            <li key={id}>
              <button
                type="button"
                aria-current={on ? "true" : undefined}
                onClick={() => onSelect(id)}
                style={{ ...s.item, ...(on ? s.itemActive : null) }}
              >
                {t(`sections.${id}`)}
              </button>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
