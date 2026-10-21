/* ContextTab — a skill's Project context for the ACTIVE repo: which repo
   documents it attaches and in which order (ContextPicker). Every change sends
   ONE PUT /skills/:id/context with the full list — optimistic, rolled back on
   error, and it never creates a skill version (attachments are not part of the
   skill's body). Any agent using the skill inherits them. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import type { Skill } from "@devdigest/shared";
import { ContextPicker } from "@/components/context-picker";
import { useSetSkillContext, useSkillContext } from "@/lib/hooks";
import { useActiveRepo } from "@/lib/repo-context";
import { serializesAs } from "./helpers";
import { s } from "./styles";

export function ContextTab({ skill }: { skill: Skill }) {
  const t = useTranslations("skills");
  const { repoId } = useActiveRepo();
  if (!repoId) return <EmptyState icon="Folder" title={t("context.noRepoTitle")} body={t("context.noRepoBody")} />;
  // Keyed by repo: switching the active repo reloads its documents and attachments (EC9).
  return <RepoContext key={repoId} skill={skill} repoId={repoId} />;
}

function RepoContext({ skill, repoId }: { skill: Skill; repoId: string }) {
  const t = useTranslations("skills");
  const tc = useTranslations("common");
  const attachments = useSkillContext(skill.id, repoId);
  const save = useSetSkillContext(skill.id, repoId);

  if (attachments.isLoading) return <Skeleton height={200} />;
  if (attachments.isError || !attachments.data) {
    return <ErrorState title={t("context.loadError")} onRetry={() => attachments.refetch()} retryLabel={tc("actions.retry")} />;
  }

  const attached = attachments.data.paths;
  return (
    <div style={s.wrap}>
      <ContextPicker
        repoId={repoId}
        attached={attached}
        onChange={(paths) => save.mutate(paths)}
        title={t("context.title")}
        badge={({ attached: k }) => t("context.badge", { attached: k })}
        hint={t("context.hint")}
        previewAs="icon"
      >
        <div style={s.label}>{t("context.serializesAs")}</div>
        <pre className="mono" style={s.block} aria-label={t("context.serializesAs")}>
          {serializesAs(attached)}
        </pre>
      </ContextPicker>
    </div>
  );
}
