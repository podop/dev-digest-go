"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { AddRepoForm } from "@/components/add-repo-form";
import type { Repo } from "@/lib/types";
import { IndexProgress } from "../IndexProgress";
import { StepFrame } from "../StepFrame";
import { s } from "./styles";

/** Step 3 (FR25): add the first repository, then watch its indexing; or, when repositories already
 *  exist, list them. Finish opens the PR list of the added (else the first) repository and stays
 *  disabled until there is one — a failed add keeps it so (EC15). */
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

  let content: React.ReactNode;
  if (added) {
    content = <IndexProgress key={added.id} repo={added} />;
  } else if (repos.length > 0) {
    content = (
      <div>
        <h2 style={s.heading}>{t("repo.existingTitle")}</h2>
        <ul style={s.list}>
          {repos.map((repo) => (
            <li key={repo.id} style={s.row}>
              {repo.full_name}
            </li>
          ))}
        </ul>
      </div>
    );
  } else {
    content = <AddRepoForm onAdded={setAdded} />;
  }

  return (
    <StepFrame
      title={t("repo.title")}
      sub={t("repo.sub")}
      onBack={onBack}
      primary={{ label: t("actions.finish"), onClick: () => target && onFinish(target.id), disabled: !target }}
    >
      {content}
    </StepFrame>
  );
}
