/* TargetStep — the one target (GitHub Actions, preselected) and the repository to add the
   workflow to, chosen among the workspace's imported repositories (FR4). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, ErrorState, FormField, Icon, Skeleton } from "@devdigest/ui";
import { Select } from "@/components/select";
import { s } from "./styles";

export function TargetStep({
  repoNames,
  loading,
  loadFailed,
  onRetry,
  repo,
  onRepoChange,
}: {
  repoNames: readonly string[];
  loading: boolean;
  loadFailed: boolean;
  onRetry: () => void;
  repo: string | null;
  onRepoChange: (name: string) => void;
}) {
  const t = useTranslations("ci.exportWizard");
  const tc = useTranslations("common");
  const options = React.useMemo(() => repoNames.map((name) => ({ value: name, label: name })), [repoNames]);

  return (
    <div style={s.body}>
      <fieldset style={s.cards}>
        <legend style={s.srOnly}>{t("targetLabel")}</legend>
        <label style={s.card}>
          <span style={s.cardHead}>
            <span style={s.cardTitle}>
              <input type="radio" name="ci-target" checked readOnly />
              <Icon.Workflow size={16} />
              {t("targets.gha")}
            </span>
            <Badge color="var(--ok)" bg="var(--bg-hover)">
              {t("recommended")}
            </Badge>
          </span>
          <span style={s.cardDesc}>{t("targets.ghaDesc")}</span>
        </label>
      </fieldset>

      {loading && <Skeleton height={36} />}
      {loadFailed && <ErrorState title={t("reposLoadError")} onRetry={onRetry} retryLabel={tc("actions.retry")} />}
      {!loading && !loadFailed && repoNames.length === 0 && (
        <p role="status" style={s.empty}>
          {t("connectRepoFirst")}
        </p>
      )}
      {!loading && !loadFailed && repoNames.length > 0 && (
        <FormField label={t("repoLabel")} hint={t("repoHint")} required>
          <Select
            value={repo ?? ""}
            onChange={onRepoChange}
            options={options}
            aria-label={t("repoLabel")}
          />
        </FormField>
      )}
    </div>
  );
}
