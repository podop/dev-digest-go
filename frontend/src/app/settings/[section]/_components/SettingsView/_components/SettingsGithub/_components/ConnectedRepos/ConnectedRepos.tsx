"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Icon } from "@devdigest/ui";
import { AddRepoForm } from "@/components/add-repo-form";
import { isGitLab } from "@/lib/code-host";
import { useRepos } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { RemoveRepoDialog } from "../../../RemoveRepoDialog";
import { RepoRow } from "../RepoRow";
import { s } from "./styles";

/** FR19 + FR37: the GitHub repos of GET /repos (GitLab ones belong to GitLab Integration), "Add repository" in place, Remove behind a confirm dialog. */
export function ConnectedRepos() {
  const t = useTranslations("githubIntegration.repos");
  const tc = useTranslations("common");
  const repos = useRepos();
  const [adding, setAdding] = React.useState(false);
  const [removing, setRemoving] = React.useState<Repo | null>(null);
  const list = (repos.data ?? []).filter((repo) => !isGitLab(repo));

  return (
    <section aria-labelledby="gh-repos-title">
      <div style={s.head}>
        <h3 id="gh-repos-title" style={s.title}>
          {t("title", { count: list.length })}
        </h3>
        {!adding && (
          <Button kind="secondary" size="sm" icon="Plus" onClick={() => setAdding(true)}>
            {t("add")}
          </Button>
        )}
      </div>

      {adding && (
        <div style={s.form}>
          <AddRepoForm onAdded={() => setAdding(false)} onCancel={() => setAdding(false)} />
        </div>
      )}

      {repos.isPending ? (
        <div role="status" style={s.muted}>
          {t("loading")}
        </div>
      ) : repos.isError ? (
        <ErrorState title={t("loadError")} onRetry={() => void repos.refetch()} retryLabel={tc("actions.retry")} />
      ) : list.length === 0 ? (
        <div style={s.muted}>{t("empty")}</div>
      ) : (
        <ul style={s.list}>
          {list.map((repo) => (
            <RepoRow key={repo.id} repo={repo} onRemove={() => setRemoving(repo)} />
          ))}
        </ul>
      )}

      <div style={s.note}>
        <Icon.Info size={13} />
        <span>{t("protectionNote")}</span>
      </div>

      {removing && <RemoveRepoDialog repo={removing} onClose={() => setRemoving(null)} />}
    </section>
  );
}
