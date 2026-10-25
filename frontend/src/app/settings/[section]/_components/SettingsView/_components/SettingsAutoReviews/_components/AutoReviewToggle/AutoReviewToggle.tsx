"use client";

import { useTranslations } from "next-intl";
import { Toggle } from "@devdigest/ui";
import { s } from "./styles";

interface AutoReviewToggleProps {
  /** Stored `automatic_reviews.enabled`. */
  value: boolean;
  /** True while a save is in flight. */
  disabled?: boolean;
  /** The requested `enabled` after a click. */
  onChange: (enabled: boolean) => void;
}

/**
 * Automatic Reviews → on/off card (FR13). Controlled: the section owns the saved value and the
 * PUT. The vendored `Toggle` has no accessible name and no `disabled` prop, so it sits in a
 * `<label>` (the name) inside a `<fieldset disabled>` (disables its button natively).
 */
export function AutoReviewToggle({ value, disabled = false, onChange }: AutoReviewToggleProps) {
  const t = useTranslations("autoReviewControls");

  return (
    <fieldset disabled={disabled} style={s.card}>
      <label style={s.row}>
        <span style={s.title}>{t("toggle.title")}</span>
        <Toggle on={value} onChange={onChange} />
      </label>
      <div style={s.subline}>{value ? t("toggle.on") : t("toggle.off")}</div>
    </fieldset>
  );
}
