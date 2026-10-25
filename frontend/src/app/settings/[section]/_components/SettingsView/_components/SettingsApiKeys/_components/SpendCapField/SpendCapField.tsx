"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, FormField, Icon, TextInput } from "@devdigest/ui";
import { useSettings, useSpend, useUpdateSettings } from "@/lib/hooks";
import {
  SPEND_CAP_MAX_DECIMALS,
  SPEND_CAP_MAX_USD,
  SPEND_CAP_MIN_USD,
} from "@devdigest/shared/constants/settings";
import { formatBound, formatMoney, monthStartDay, parseCap } from "./helpers";
import { s } from "./styles";

/**
 * API Keys → Monthly spend cap (FR14): a dollar field saved as
 * `settings.monthly_spend_cap_usd` (empty = no cap) plus the month's spend from
 * GET /settings/spend and a warning once the cap is reached.
 */
export function SpendCapField() {
  const t = useTranslations("settingsBudget");
  const errorId = React.useId();
  const { data: settings } = useSettings();
  const spend = useSpend();
  const update = useUpdateSettings();

  const savedCap = settings?.monthly_spend_cap_usd ?? null;
  // null = show the saved cap; a string = what the user has typed since the last save.
  const [draft, setDraft] = React.useState<string | null>(null);
  const text = draft ?? (savedCap === null ? "" : String(savedCap));
  const parsed = parseCap(text);
  const nextCap = parsed.kind === "value" ? parsed.value : null;
  const dirty = draft !== null && parsed.kind !== "error" && nextCap !== savedCap;
  const canSave = dirty && !update.isPending;

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSave) return;
    update.mutate({ monthly_spend_cap_usd: nextCap }, { onSuccess: () => setDraft(null) });
  };

  const status = spend.data;
  const spent = status ? formatMoney(status.spent_usd) : "";
  const hint = status
    ? t(status.cap_usd === null ? "spendCap.hintNoCap" : "spendCap.hint", { spent })
    : spend.isError
      ? undefined
      : t("spendCap.hintLoading");
  const inputError =
    parsed.kind === "error"
      ? t(`spendCap.errors.${parsed.error}`, {
          max: parsed.error === "decimals" ? SPEND_CAP_MAX_DECIMALS : formatBound(SPEND_CAP_MAX_USD),
          min: formatBound(SPEND_CAP_MIN_USD),
        })
      : null;

  return (
    <FormField label={t("spendCap.label")} hint={hint}>
      <form onSubmit={save} style={s.row} noValidate>
        <div style={s.input}>
          <TextInput
            value={text}
            onChange={setDraft}
            mono
            inputMode="decimal"
            placeholder={t("spendCap.placeholder")}
            aria-label={t("spendCap.inputAria")}
            aria-invalid={inputError !== null}
            aria-describedby={inputError ? errorId : undefined}
          />
        </div>
        <Button kind="secondary" size="md" type="submit" disabled={!canSave}>
          {update.isPending ? t("spendCap.saving") : t("spendCap.save")}
        </Button>
      </form>
      {inputError && (
        <div id={errorId} role="alert" style={s.error}>
          {inputError}
        </div>
      )}
      {update.isError && (
        <div role="alert" style={s.error}>
          {t("spendCap.saveFailed")}
        </div>
      )}
      {spend.isError && (
        <div style={s.unavailable}>
          <span>{t("spendCap.unavailable")}</span>
          <Button kind="secondary" size="sm" onClick={() => spend.refetch()}>
            {t("spendCap.retry")}
          </Button>
        </div>
      )}
      {status && status.cap_usd !== null && status.remaining_usd !== null && (
        <div style={s.summary}>
          {t("spendCap.remaining", {
            remaining: formatMoney(status.remaining_usd),
            cap: formatMoney(status.cap_usd),
          })}
        </div>
      )}
      {status?.cap_reached && status.cap_usd !== null && (
        <div role="note" style={s.banner}>
          <Icon.AlertTriangle size={15} style={s.bannerIcon} />
          <span>
            {t("spendCap.reached", {
              cap: formatMoney(status.cap_usd),
              spent,
              since: monthStartDay(status.month_start),
            })}
          </span>
        </div>
      )}
    </FormField>
  );
}
