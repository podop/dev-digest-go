"use client";

import { useTranslations } from "next-intl";
import { FormField } from "@devdigest/ui";
import { Select } from "@/components/select";
import { intervalChoices, parseMinutes } from "./helpers";
import { s } from "./styles";

interface IntervalSelectProps {
  /** Stored `polling_interval_min`, in minutes. */
  value: number;
  /** True while a save is in flight. */
  disabled?: boolean;
  /** The requested `polling_interval_min` after a pick. */
  onChange: (minutes: number) => void;
}

/**
 * Automatic Reviews → "Polling interval" (FR14). Controlled: the section owns the saved value
 * and the PUT. A stored value outside the seven choices is listed as an extra option.
 */
export function IntervalSelect({ value, disabled = false, onChange }: IntervalSelectProps) {
  const t = useTranslations("autoReviewControls");

  const label = (minutes: number) => {
    if (minutes === 1) return t("interval.everyMinute");
    if (minutes === 60) return t("interval.everyHour");
    return t("interval.everyMinutes", { minutes });
  };
  const options = intervalChoices(value).map((minutes) => ({ value: String(minutes), label: label(minutes) }));

  const pick = (next: string) => {
    const minutes = parseMinutes(next);
    if (minutes !== undefined && minutes !== value) onChange(minutes);
  };

  return (
    <FormField label={t("interval.label")} hint={t("interval.hint")}>
      <div style={s.select}>
        <Select
          value={String(value)}
          onChange={pick}
          options={options}
          aria-label={t("interval.selectAria")}
          disabled={disabled}
          mono={false}
        />
      </div>
    </FormField>
  );
}
