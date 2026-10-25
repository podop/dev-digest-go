"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { FormField, Icon } from "@devdigest/ui";
import { Select } from "@/components/select";
import { useSettings, useUpdateSettings } from "@/lib/hooks";
import { DAY_VALUES, FOREVER, NO_CHOICE, toRetentionDays, toSelectValue } from "./helpers";
import { s } from "./styles";

/**
 * Workspace → Retention (FR10): how long run traces are kept, saved on change as
 * `settings.retention_days` (absent = 90, null = keep forever). The shown value comes
 * from the settings cache, so a failed save leaves the previous value in place.
 */
export function RetentionField() {
  const t = useTranslations("workspaceRetention");
  const { data: settings } = useSettings();
  const update = useUpdateSettings();

  if (settings === undefined) {
    return <FormField label={t("label")} hint={t("loading")} />;
  }

  const stored = toSelectValue(settings.retention_days as unknown);
  // While a save is in flight show the requested choice; on failure fall back to the stored one.
  const requested = update.isPending && update.variables ? toSelectValue(update.variables.retention_days) : null;
  const value = requested ?? stored;

  const save = (next: string) => {
    const days = toRetentionDays(next);
    if (days !== undefined) update.mutate({ retention_days: days });
  };

  const options = [
    ...DAY_VALUES.map((days) => ({ value: days, label: t("days", { days }) })),
    { value: FOREVER, label: t("forever") },
  ];

  return (
    <FormField label={t("label")} hint={t("hint")}>
      <div style={s.select}>
        <Select
          value={value}
          onChange={save}
          options={options}
          aria-label={t("selectAria")}
          disabled={update.isPending}
          mono={false}
          formatValue={(label) => (value === NO_CHOICE ? t("choose") : label)}
        />
      </div>
      {stored === NO_CHOICE && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={14} />
          <span>{t("invalid")}</span>
        </div>
      )}
      {update.isError && (
        <div role="alert" style={s.error}>
          {t("saveFailed")}
        </div>
      )}
    </FormField>
  );
}
