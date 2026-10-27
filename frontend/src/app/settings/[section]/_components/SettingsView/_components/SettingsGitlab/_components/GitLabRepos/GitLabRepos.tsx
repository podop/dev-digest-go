/* GitLabRepos — the workspace's GitLab repositories (FR37): GET /repos filtered to GitLab, "Add repository" under
   the list (in place), Remove behind the shared confirm dialog — the only place a GitLab repository is removed (it names the host and says "Nothing changes on GitLab."). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState } from "@devdigest/ui";
import { AddRepoForm } from "@/components/add-repo-form";
import { isGitLab } from "@/lib/code-host";
import { useRepos } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { RemoveRepoDialog } from "../../../RemoveRepoDialog";
import { GitLabRepoRow } from "./GitLabRepoRow";
import { s } from "./styles";

export function GitLabRepos() {
  const t = useTranslations("gitlabIntegration.repos");
  const repos = useRepos();
  const [adding, setAdding] = React.useState(false);
  const [removing, setRemoving] = React.useState<Repo | null>(null);
  const list = (repos.data ?? []).filter((repo) => isGitLab(repo));

  return (
    <section aria-labelledby="gl-repos-title" style={s.field}>
      <h3 id="gl-repos-title" style={s.title}>
        {t("title", { count: list.length })}
      </h3>

      {repos.isPending ? (
        <div role="status" style={s.muted}>
          {t("loading")}
        </div>
      ) : repos.isError ? (
        <ErrorState title={t("loadError")} onRetry={() => void repos.refetch()} retryLabel={t("retry")} />
      ) : list.length === 0 ? (
        <div style={s.muted}>{t("empty")}</div>
      ) : (
        <ul style={s.list}>
          {list.map((repo) => (
            <GitLabRepoRow key={repo.id} repo={repo} onRemove={() => setRemoving(repo)} />
          ))}
        </ul>
      )}

      {adding ? (
        <div style={s.form}>
          <AddRepoForm section="gitlab" onAdded={() => setAdding(false)} onCancel={() => setAdding(false)} />
        </div>
      ) : (
        <div style={s.add}>
          <Button type="button" kind="secondary" size="sm" icon="Plus" onClick={() => setAdding(true)}>
            {t("add")}
          </Button>
        </div>
      )}

      {removing && <RemoveRepoDialog repo={removing} onClose={() => setRemoving(null)} />}
    </section>
  );
}
