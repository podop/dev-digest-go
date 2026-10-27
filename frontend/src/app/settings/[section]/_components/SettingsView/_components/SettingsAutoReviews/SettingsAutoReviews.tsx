/* SettingsAutoReviews — Settings → Automatic Reviews (FR13–FR18, FR20; GitLab A FR13 subtitle; GitLab C FR47 info box). The section owns the
   data: it reads the settings and the agents, and every control saves at once with
   PUT /settings. Controls render the stored value, so a failed save returns them to it (the
   global mutation toast shows the API message). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { ErrorState, Skeleton } from "@devdigest/ui";
import { Field, InfoNote, SectionPane } from "@/components/settings-kit";
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
      <SectionPane>
        {title}
        <ErrorState title={t("loadError")} onRetry={() => void settings.refetch()} retryLabel={tc("actions.retry")} />
      </SectionPane>
    );
  }
  if (!settings.data) {
    return (
      <SectionPane>
        <div style={s.loading} role="status" aria-label={t("loading")}>
          {title}
          <Skeleton height={64} />
          <Skeleton height={34} />
          <Skeleton height={34} />
        </div>
      </SectionPane>
    );
  }

  const automatic = settings.data.automatic_reviews;
  const saving = update.isPending;

  return (
    <SectionPane>
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
        <Field label={ta("label")}>
          <ErrorState title={t("agentsLoadError")} onRetry={() => void agents.refetch()} retryLabel={tc("actions.retry")} />
        </Field>
      ) : !agents.data ? (
        <Field label={ta("label")}>
          <div style={s.chipsSkeleton} role="status" aria-label={t("agentsLoading")}>
            <Skeleton width={96} height={26} />
            <Skeleton width={96} height={26} />
            <Skeleton width={96} height={26} />
          </div>
        </Field>
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
      <InfoNote>
        {t.rich(pollsGitLab ? "infoCiHosts" : "info", {
          integrations: (chunks) => (
            <Link href={sectionHref("integrations")} style={s.link}>
              {chunks}
            </Link>
          ),
        })}
      </InfoNote>
    </SectionPane>
  );
}
