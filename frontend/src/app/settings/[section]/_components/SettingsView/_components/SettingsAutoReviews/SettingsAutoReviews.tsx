/* SettingsAutoReviews — Settings → Automatic Reviews (FR13–FR18, FR20; GitLab A FR13 subtitle). The section owns the
   data: it reads the settings and the agents, and every control saves at once with
   PUT /settings. Controls render the stored value, so a failed save returns them to it (the
   global mutation toast shows the API message). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { ErrorState, FormField, Icon, Skeleton } from "@devdigest/ui";
import { isGitLab } from "@/lib/code-host";
import { useAgents, useRepos, useSettings, useUpdateSettings } from "@/lib/hooks";
import { sectionHref } from "../../constants";
import { SectionTitle } from "../SectionTitle";
import { AgentChips } from "./_components/AgentChips";
import { AutoReviewStatus } from "./_components/AutoReviewStatus";
import { AutoReviewToggle } from "./_components/AutoReviewToggle";
import { IntervalSelect } from "./_components/IntervalSelect";
import { TriggerConditions } from "./_components/TriggerConditions";
import { intervalOf, triggersOf } from "./helpers";
import { s } from "./styles";

export function SettingsAutoReviews() {
  const t = useTranslations("automaticReviews");
  const ta = useTranslations("autoReviewAgents");
  const tc = useTranslations("common");
  const settings = useSettings();
  const agents = useAgents();
  const update = useUpdateSettings();
  const repos = useRepos();
  // GitLab A FR13: the copy names GitHub until a GitLab repository exists (a failed list keeps it).
  const pollsGitLab = repos.data?.some(isGitLab) ?? false;

  const title = <SectionTitle title={t("title")} body={t(pollsGitLab ? "bodyCodeHosts" : "body")} />;

  if (settings.isError) {
    return (
      <div style={s.wrap}>
        {title}
        <ErrorState title={t("loadError")} onRetry={() => void settings.refetch()} retryLabel={tc("actions.retry")} />
      </div>
    );
  }
  if (!settings.data) {
    return (
      <div style={s.wrap} role="status" aria-label={t("loading")}>
        {title}
        <Skeleton height={64} />
        <Skeleton height={36} />
        <Skeleton height={36} />
      </div>
    );
  }

  const automatic = settings.data.automatic_reviews;
  const saving = update.isPending;

  return (
    <div style={s.wrap}>
      {title}
      <AutoReviewToggle
        value={automatic.enabled}
        disabled={saving}
        onChange={(enabled) => update.mutate({ automatic_reviews: { enabled } })}
      />
      <IntervalSelect
        value={intervalOf(settings.data)}
        disabled={saving}
        onChange={(polling_interval_min) => update.mutate({ polling_interval_min })}
      />
      {agents.isError ? (
        <FormField label={ta("label")}>
          <ErrorState title={t("agentsLoadError")} onRetry={() => void agents.refetch()} retryLabel={tc("actions.retry")} />
        </FormField>
      ) : !agents.data ? (
        <FormField label={ta("label")}>
          <div style={s.chipsSkeleton} role="status" aria-label={t("agentsLoading")}>
            <Skeleton width={96} height={30} />
            <Skeleton width={96} height={30} />
            <Skeleton width={96} height={30} />
          </div>
        </FormField>
      ) : (
        <AgentChips
          agents={agents.data}
          selected={automatic.agent_ids}
          disabled={saving}
          onChange={(agent_ids) => update.mutate({ automatic_reviews: { agent_ids } })}
        />
      )}
      <TriggerConditions
        value={triggersOf(automatic)}
        disabled={saving}
        onChange={(patch) => update.mutate({ automatic_reviews: patch })}
      />
      <AutoReviewStatus />
      <div style={s.info}>
        <Icon.Info size={14} aria-hidden />
        <span>
          {t.rich("info", {
            integrations: (chunks) => (
              <Link href={sectionHref("integrations")} style={s.link}>
                {chunks}
              </Link>
            ),
          })}
        </span>
      </div>
    </div>
  );
}
