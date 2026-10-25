"use client";

import { useTranslations } from "next-intl";
import { Checkbox, FormField } from "@devdigest/ui";
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
 * and the PUT. The vendored `Checkbox` has no `disabled` prop, so the group is a
 * `<fieldset disabled>`, which disables its buttons natively.
 */
export function TriggerConditions({ value, disabled = false, onChange }: TriggerConditionsProps) {
  const t = useTranslations("autoReviewControls");

  return (
    <FormField label={t("triggers.label")} hint={t("triggers.hint")}>
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
    </FormField>
  );
}
