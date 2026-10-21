/* SectionCard — one collapsible card of the tour: icon + title header (a button that toggles the
   body) and the section content. Its `id` is the TOC / share-link anchor. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon, type IconName } from "@devdigest/ui";
import { s } from "./styles";

export function SectionCard({
  id,
  title,
  icon,
  children,
}: {
  id: string;
  title: string;
  icon: IconName;
  children: React.ReactNode;
}) {
  const t = useTranslations("onboarding");
  const [open, setOpen] = React.useState(true);
  const bodyId = `${id}-body`;
  const I = Icon[icon];

  return (
    <section id={id} aria-labelledby={`${id}-title`} style={s.card}>
      <h2 id={`${id}-title`} style={s.heading}>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          aria-label={t("collapse", { title })}
          onClick={() => setOpen((v) => !v)}
          style={s.head}
        >
          <span aria-hidden style={s.icon}>
            <I size={16} />
          </span>
          <span style={s.title}>{title}</span>
          <Icon.ChevronDown size={16} style={{ ...s.chevron, ...(open ? null : s.chevronClosed) }} />
        </button>
      </h2>
      {open && (
        <div id={bodyId} style={s.body}>
          {children}
        </div>
      )}
    </section>
  );
}
