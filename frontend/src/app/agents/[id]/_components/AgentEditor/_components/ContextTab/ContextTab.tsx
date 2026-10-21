/* ContextTab — the agent's Project context for the ACTIVE repo: which repo
   documents are attached and in which order (ContextPicker). Every change
   sends ONE PUT /agents/:id/context with the full list — optimistic, rolled
   back on error, no agent version bump. The footer estimates what a run of this
   agent adds in this repo: its own attachments plus those of its enabled
   skills, de-duplicated (FR13), and warns past the run budget. */
"use client";

import type React from "react";
import { useTranslations } from "next-intl";
import { EmptyState, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import type { Agent } from "@devdigest/shared";
import { PROJECT_CONTEXT_BUDGET_TOKENS } from "@devdigest/shared/constants/project-context";
import { ContextPicker } from "@/components/context-picker";
import {
  useAgentContext,
  useAgentSkillLinks,
  useContextDocs,
  useSetAgentContext,
  useSkills,
  useSkillsContext,
} from "@/lib/hooks";
import { useActiveRepo } from "@/lib/repo-context";
import { enabledSkillIds, runTokens } from "./helpers";
import { s } from "./styles";

const code = (chunks: React.ReactNode) => <code className="mono">{chunks}</code>;

export function ContextTab({ agent }: { agent: Agent }) {
  const t = useTranslations("agents");
  const { repoId } = useActiveRepo();
  if (!repoId) return <EmptyState icon="Folder" title={t("context.noRepoTitle")} body={t("context.noRepoBody")} />;
  // Keyed by repo: switching the active repo reloads its documents and attachments (EC9).
  return <RepoContext key={repoId} agent={agent} repoId={repoId} />;
}

function RepoContext({ agent, repoId }: { agent: Agent; repoId: string }) {
  const t = useTranslations("agents");
  const tc = useTranslations("common");
  const attachments = useAgentContext(agent.id, repoId);
  const save = useSetAgentContext(agent.id, repoId);
  const list = useContextDocs(repoId);
  const skills = useSkills();
  const links = useAgentSkillLinks(agent.id);
  const skillIds = enabledSkillIds(skills.data ?? [], links.data ?? []);
  const skillsContext = useSkillsContext(skillIds, repoId);

  if (attachments.isLoading) return <Skeleton height={200} />;
  if (attachments.isError || !attachments.data) {
    return <ErrorState title={t("context.loadError")} onRetry={() => attachments.refetch()} retryLabel={tc("actions.retry")} />;
  }

  const attached = attachments.data.paths;
  const { total, fromSkills } = runTokens(
    list.data?.docs ?? [],
    attached,
    skillIds.map((id) => skillsContext.byId[id] ?? []),
  );

  return (
    <div style={s.wrap}>
      <ContextPicker
        repoId={repoId}
        attached={attached}
        onChange={(paths) => save.mutate(paths)}
        title={t("context.title")}
        badge={({ attached: k, total: n }) => t("context.badge", { attached: k, total: n })}
        hint={t.rich("context.hint", { code })}
        previewAs="button"
      >
        {list.data?.clone_status === "ready" && (
          <div style={s.footer}>
            <span className="tnum" style={s.total}>
              {t("context.total", { tokens: total })}
            </span>
            {fromSkills > 0 && <span className="tnum">{t("context.fromSkills", { tokens: fromSkills })}</span>}
            <span style={s.note}>{t.rich("context.injected", { code })}</span>
            {total > PROJECT_CONTEXT_BUDGET_TOKENS && (
              <p role="status" style={s.warning}>
                <Icon.AlertTriangle size={14} />
                {t("context.overBudget", { budget: PROJECT_CONTEXT_BUDGET_TOKENS })}
              </p>
            )}
          </div>
        )}
      </ContextPicker>
    </div>
  );
}
