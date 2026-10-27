"use client";

import { useTranslations } from "next-intl";
import type React from "react";
import { Icon } from "@devdigest/ui";
import { Field } from "@/components/settings-kit";
import { s } from "./styles";

/** The two trigger flags of `automatic_reviews`. */
export interface Triggers {
  on_new_pr: boolean;
  on_new_commits: boolean;
}

interface TriggerConditionsProps {
  /** Stored `automatic_reviews.on_new_pr` / `on_new_commits`. */
  value: Triggers;
  /** True while a save is in flight. */
  disabled?: boolean;
  /** The one flag that changed, as a PUT patch. */
  onChange: (patch: Partial<Triggers>) => void;
}

/**
 * Automatic Reviews → "Trigger conditions" (FR16). Controlled: the section owns the saved value
 * and the PUT. The group is a `<fieldset disabled>`, which disables its buttons natively. The
 * checkbox is the prototype's (16px box, 13px label, 9px gap), smaller than the vendored one.
 */
export function TriggerConditions({ value, disabled = false, onChange }: TriggerConditionsProps) {
  const t = useTranslations("autoReviewControls");

  return (
    <Field label={t("triggers.label")} hint={t("triggers.hint")}>
      <fieldset disabled={disabled} aria-label={t("triggers.groupAria")} style={s.group}>
        <Checkbox
          checked={value.on_new_pr}
          onChange={(on_new_pr) => onChange({ on_new_pr })}
          label={t("triggers.onNewPr")}
        />
        <Checkbox
          checked={value.on_new_commits}
          onChange={(on_new_commits) => onChange({ on_new_commits })}
          label={t("triggers.onNewCommits")}
        />
      </fieldset>
    </Field>
  );
}

/** A native checkbox drawn as the prototype's box: the input is transparent on top of the box and the check. */
function Checkbox({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: React.ReactNode;
}) {
  return (
    <label style={s.option}>
      <span style={s.box(checked)}>
        <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} style={s.input} />
        {checked && <Icon.Check size={11} style={s.check} aria-hidden />}
      </span>
      {label}
    </label>
  );
}
