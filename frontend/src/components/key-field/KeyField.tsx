"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Icon, TextInput } from "@devdigest/ui";
import type { ConnTestProvider, ConnTestResult } from "@/lib/types";
import { applyMaskedEdit, maskKey } from "./helpers";
import { ResultBanner } from "./ResultBanner";
import { StatusBadge, type KeyStatus } from "./StatusBadge";
import { useKeyTest } from "./useKeyTest";
import { s } from "./styles";

export interface KeyFieldProps {
  provider: ConnTestProvider;
  label: string;
  hint?: React.ReactNode;
  /** Page where the user creates the key (see `KEY_PAGE_URLS`). */
  docsHref: string;
  /** Whether a key is stored; `undefined` while that is still loading (no badge yet). */
  configured: boolean | undefined;
  optional?: boolean;
  /** Run one test without a key when the field opens with a stored key (FR17). */
  autoTest?: boolean;
  /** Called with every test outcome, so a section can show provider-specific extras (login, scopes). */
  onResult?: (result: ConnTestResult) => void;
}

function statusOf(configured: boolean, result: ConnTestResult | null): KeyStatus {
  if (result && !result.ok) return "failed";
  return configured || result?.ok ? "connected" : "notConnected";
}

/**
 * One provider key: label, key-page link, masked key input with a reveal toggle, status badge,
 * Test connection and its result banner (FR2–FR4, FR6). The typed key stays in this component's
 * state — never in the URL, storage or logs (NFR7).
 */
export function KeyField({ provider, label, hint, docsHref, configured, optional, autoTest, onResult }: KeyFieldProps) {
  const t = useTranslations("keyField");
  const id = React.useId();
  const [key, setKey] = React.useState("");
  const [reveal, setReveal] = React.useState(false);
  const { run, result, pending } = useKeyTest(provider);

  const test = React.useCallback(
    async (typed: string) => {
      const next = await run(typed);
      if (next) onResult?.(next);
    },
    [run, onResult],
  );

  const autoTested = React.useRef(false);
  React.useEffect(() => {
    if (!autoTest || !configured || autoTested.current) return;
    autoTested.current = true;
    void test("");
  }, [autoTest, configured, test]);

  const placeholder = configured ? t("placeholderSaved") : t("placeholderEmpty");

  return (
    <div style={s.wrap}>
      <div style={s.head}>
        <label htmlFor={id} style={s.label}>
          {label}
        </label>
        {optional && <span style={s.optional}>{t("optional")}</span>}
        <a href={docsHref} target="_blank" rel="noopener noreferrer" style={s.docs}>
          {t(provider === "github" ? "docs.token" : "docs.key")}
          <Icon.ExternalLink size={12} />
        </a>
        <div style={s.spacer} />
        {configured !== undefined && <StatusBadge status={statusOf(configured, result)} />}
      </div>
      <div style={s.row}>
        <div style={s.input}>
          <TextInput
            id={id}
            mono
            value={reveal ? key : maskKey(key)}
            onChange={(v) => setKey(reveal ? v : applyMaskedEdit(key, v))}
            placeholder={placeholder}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            data-1p-ignore
            data-lpignore="true"
            suffix={
              <button
                type="button"
                style={s.toggle}
                aria-label={t("reveal")}
                aria-pressed={reveal}
                onClick={() => setReveal((r) => !r)}
              >
                {reveal ? <Icon.Eye size={14} /> : <Icon.EyeOff size={14} />}
              </button>
            }
          />
        </div>
        <Button kind="secondary" size="md" onClick={() => void test(key)} disabled={pending}>
          {pending ? t("testing") : t("test")}
        </Button>
      </div>
      {hint && <div style={s.hint}>{hint}</div>}
      {result && !pending && <ResultBanner result={result} />}
    </div>
  );
}
