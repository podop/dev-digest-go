/* TargetStep — the target (GitHub Actions or GitLab CI, following the chosen repository's host)
   and the repository to add the job to, chosen among the workspace's imported repositories
   (FR4, GitLab C FR40). The card that does not fit the repository is disabled with its reason. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import type { Repo } from "@/lib/types";
import { RepoPicker } from "../RepoPicker";
import { s } from "./styles";

export function TargetStep({
  repos,
  loading,
  loadFailed,
  onRetry,
  repoId,
  gitlab,
  onRepoChange,
}: {
  repos: readonly Repo[];
  loading: boolean;
  loadFailed: boolean;
  onRetry: () => void;
  /** The selected repository's id; null while none is selected. */
  repoId: string | null;
  /** The selected repository lives on GitLab. */
  gitlab: boolean;
  onRepoChange: (id: string) => void;
}) {
  const t = useTranslations("ci.exportWizard");
  const tc = useTranslations("common");
  const picked = repoId != null;

  return (
    <div style={s.body}>
      <fieldset style={s.cards}>
        <legend style={s.srOnly}>{t("targetLabel")}</legend>
        <label style={picked && gitlab ? { ...s.card, ...s.cardOff } : s.card}>
          <span style={s.cardHead}>
            <span style={s.cardTitle}>
              <input type="radio" name="ci-target" checked={!gitlab} disabled={picked && gitlab} readOnly />
              <Icon.Workflow size={16} />
              {t("targets.gha")}
            </span>
            {!gitlab && (
              <Badge color="var(--ok)" bg="var(--bg-hover)">
                {t("recommended")}
              </Badge>
            )}
          </span>
          <span style={s.cardDesc}>{picked && gitlab ? t("needsGithubRepo") : t("targets.ghaDesc")}</span>
        </label>
        <label style={picked && !gitlab ? { ...s.card, ...s.cardOff } : s.card}>
          <span style={s.cardHead}>
            <span style={s.cardTitle}>
              <input type="radio" name="ci-target" checked={gitlab} disabled={picked && !gitlab} readOnly />
              <Icon.Workflow size={16} />
              {t("gitlab.target")}
            </span>
            {gitlab && (
              <Badge color="var(--ok)" bg="var(--bg-hover)">
                {t("recommended")}
              </Badge>
            )}
          </span>
          <span style={s.cardDesc}>{picked && !gitlab ? t("gitlab.needsRepo") : t("gitlab.targetDesc")}</span>
        </label>
      </fieldset>

      {loading && <Skeleton height={36} />}
      {loadFailed && <ErrorState title={t("reposLoadError")} onRetry={onRetry} retryLabel={tc("actions.retry")} />}
      {!loading && !loadFailed && repos.length === 0 && (
        <p role="status" style={s.empty}>
          {t("connectRepoFirst")}
        </p>
      )}
      {!loading && !loadFailed && repos.length > 0 && (
        <RepoPicker repos={repos} selectedId={repoId} onChange={onRepoChange} />
      )}
    </div>
  );
}
