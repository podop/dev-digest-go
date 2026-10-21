/* PeriodPicker — the period control of the Agent Performance dashboard and the agent Stats tab
   (FR11): a Select ("Last 24 hours" / "Last 30 days" / "Custom range…"), the custom-range form
   (two labelled date inputs, inline errors, Apply disabled until the range is valid) and the label
   of the applied period. Controlled by the caller, which owns the URL; no data fetching here. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, TextInput } from "@devdigest/ui";
import { Select } from "@/components/select";
import { customRangeError, formatPeriodRange, type StatsPeriod } from "@/lib/stats-period";
import { s } from "./styles";

type PickerValue = "1d" | "30d" | "custom";

export interface PeriodPickerProps {
  period: StatsPeriod;
  onChange: (next: StatsPeriod) => void;
}

export function PeriodPicker({ period, onChange }: PeriodPickerProps) {
  const t = useTranslations("agentPerformance.period");
  const [editing, setEditing] = React.useState(false);
  const [from, setFrom] = React.useState(period.key === "custom" ? period.from : "");
  const [to, setTo] = React.useState(period.key === "custom" ? period.to : "");

  const options = React.useMemo(
    () => [
      { value: "1d" as const, label: t("last24h") },
      { value: "30d" as const, label: t("last30d") },
      { value: "custom" as const, label: t("custom") },
    ],
    [t],
  );
  const complete = from !== "" && to !== "";
  const error = complete ? customRangeError(from, to, new Date()) : null;
  const label = formatPeriodRange(period, { "1d": t("last24h"), "30d": t("last30d") });

  const pick = (value: PickerValue) => {
    if (value === "custom") {
      if (period.key === "custom") {
        setFrom(period.from);
        setTo(period.to);
      }
      setEditing(true);
      return;
    }
    setEditing(false);
    if (period.key !== value) onChange({ key: value });
  };

  const apply = (e: React.FormEvent) => {
    e.preventDefault();
    if (!complete || error) return;
    setEditing(false);
    onChange({ key: "custom", from, to });
  };

  return (
    <div style={s.wrap}>
      <div style={s.bar}>
        <span style={s.select}>
          <Select
            value={editing ? "custom" : period.key}
            options={options}
            mono={false}
            icon="Calendar"
            chevron="none"
            aria-label={t("label")}
            onChange={pick}
          />
        </span>
        <span style={s.label}>{label}</span>
      </div>
      {editing && (
        <form style={s.form} onSubmit={apply}>
          <label style={s.field}>
            {t("startDate")}
            <TextInput type="date" value={from} onChange={setFrom} aria-invalid={error != null || undefined} />
          </label>
          <label style={s.field}>
            {t("endDate")}
            <TextInput type="date" value={to} onChange={setTo} aria-invalid={error != null || undefined} />
          </label>
          <span style={s.apply}>
            <Button type="submit" kind="primary" disabled={!complete || error != null}>
              {t("apply")}
            </Button>
          </span>
          {error && (
            <p role="alert" style={s.error}>
              {t(`errors.${error}`)}
            </p>
          )}
        </form>
      )}
    </div>
  );
}
