"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { AddRepoForm } from "@/components/add-repo-form";
import { KitBadge } from "@/components/settings-kit";
import type { Repo } from "@/lib/types";
import { IndexProgress } from "../IndexProgress";
import { StepFrame } from "../StepFrame";
import { s } from "./styles";

/** Step 3 (FR25): add a repository and watch its indexing. Existing repositories are listed, and the
 *  add form is always there ("Add another repository" once there is a list or a just-added repo), so
 *  there is always a way to add one. Finish opens the PR list of the last added (else the first)
 *  repository and stays disabled until there is one — a failed add keeps it so (EC15). */
export function RepoStep({
  repos,
  onBack,
  onFinish,
}: {
  repos: readonly Repo[];
  onBack?: () => void;
  onFinish: (repoId: string) => void;
}) {
  const t = useTranslations("setup");
  const [added, setAdded] = React.useState<Repo | null>(null);
  const target = added ?? repos[0] ?? null;
  // The just-added repository is shown by its indexing box, not twice.
  const existing = repos.filter((repo) => repo.id !== added?.id);

  return (
    <StepFrame
      title={t("repo.title")}
      sub={t("repo.sub")}
      onBack={onBack}
      primary={{ label: t("actions.finish"), onClick: () => target && onFinish(target.id), disabled: !target }}
    >
      {existing.length > 0 && (
        <div>
          <h4 style={s.heading}>{t("repo.existingTitle")}</h4>
          <ul style={s.list}>
            {existing.map((repo) => (
              <li key={repo.id} style={s.row}>
                <Icon.GitBranch size={13} style={s.rowIcon} aria-hidden />
                <span className="mono" style={s.rowName}>
                  {repo.full_name}
                </span>
                {repo.default_branch && <KitBadge mono>{repo.default_branch}</KitBadge>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {added && <IndexProgress key={added.id} repo={added} />}
      {(existing.length > 0 || added) && <h4 style={s.heading}>{t("repo.addAnotherTitle")}</h4>}
      <AddRepoForm onAdded={setAdded} />
    </StepFrame>
  );
}
