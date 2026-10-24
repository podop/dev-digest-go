/* AddRepoForm — the add-repository form (GitHub URL only; API keys live in Settings → API
   Keys). Shared by Settings → GitHub Integration and the first-run Setup: the host decides
   what happens after an add (`onAdded`) and whether there is a Cancel (`onCancel`). It shows
   the API's message when the add fails (bad URL, 409 duplicate, clone error) and keeps the
   URL so it can be fixed. Focus goes to the URL field on open and back to it after a failure. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, FormField, Icon, TextInput } from "@devdigest/ui";
import { useAddRepo } from "@/lib/hooks";
import { ApiError } from "@/lib/api";
import type { Repo } from "@/lib/types";

export function AddRepoForm({ onAdded, onCancel }: { onAdded: (repo: Repo) => void; onCancel?: () => void }) {
  const t = useTranslations("addRepo");
  const [repoUrl, setRepoUrl] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);
  const formRef = React.useRef<HTMLFormElement>(null);
  const addRepo = useAddRepo();
  const url = repoUrl.trim();

  const focusUrl = () => formRef.current?.querySelector("input")?.focus();
  // Focus the field when the form opens (external DOM state, so an effect is right here).
  React.useEffect(focusUrl, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!url || addRepo.isPending) return;
    setError(null);
    try {
      const repo = await addRepo.mutateAsync(url);
      setRepoUrl("");
      onAdded(repo);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("addError"));
      focusUrl();
    }
  };

  return (
    <form ref={formRef} onSubmit={submit} noValidate>
      <FormField label={t("urlLabel")} hint={t("urlHint")}>
        <TextInput
          value={repoUrl}
          onChange={setRepoUrl}
          mono
          aria-label={t("urlLabel")}
          aria-invalid={error != null}
          placeholder={t("urlPlaceholder")}
        />
      </FormField>

      {error && (
        <div
          role="alert"
          style={{
            display: "flex",
            alignItems: "center",
            gap: 12,
            padding: "12px 14px",
            borderRadius: 8,
            background: "var(--crit-bg)",
            border: "1px solid rgba(239,68,68,0.25)",
            marginTop: 16,
          }}
        >
          <Icon.XCircle size={16} style={{ color: "var(--crit)" }} />
          <span style={{ fontSize: 13, color: "var(--text-secondary)" }}>{error}</span>
        </div>
      )}

      <div style={{ display: "flex", gap: 12, alignItems: "center", marginTop: 24 }}>
        {onCancel && (
          <Button kind="ghost" size="md" type="button" onClick={onCancel}>
            {t("cancel")}
          </Button>
        )}
        <div style={{ flex: 1 }} />
        <Button kind="primary" size="md" icon="Plus" type="submit" disabled={!url || addRepo.isPending}>
          {addRepo.isPending ? t("submitting") : t("submit")}
        </Button>
      </div>
    </form>
  );
}
