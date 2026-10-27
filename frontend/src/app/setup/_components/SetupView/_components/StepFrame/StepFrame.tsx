"use client";

import type React from "react";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { s } from "./styles";

export interface StepAction {
  label: string;
  onClick: () => void;
  disabled?: boolean;
}

/** Title, sub, body and the Back / primary action row shared by the three steps. Back is always
 *  shown as in the prototype; without `onBack` (step 1) it is disabled. */
export function StepFrame({
  title,
  sub,
  onBack,
  primary,
  children,
}: {
  title: string;
  sub: string;
  onBack?: () => void;
  primary: StepAction;
  children: React.ReactNode;
}) {
  const t = useTranslations("setup");
  return (
    <section aria-label={title}>
      <h1 style={s.title}>{title}</h1>
      <p style={s.sub}>{sub}</p>
      {children}
      <div style={s.actions}>
        <Button kind="ghost" size="md" onClick={onBack} disabled={!onBack}>
          {t("actions.back")}
        </Button>
        <div style={s.spacer} />
        <Button kind="primary" size="md" iconRight="ArrowRight" onClick={primary.onClick} disabled={primary.disabled}>
          {primary.label}
        </Button>
      </div>
    </section>
  );
}
