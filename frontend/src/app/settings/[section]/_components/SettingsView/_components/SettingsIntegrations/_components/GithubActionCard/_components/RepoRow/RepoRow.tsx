/* RepoRow — one repository of the GitHub Action card: the name, the last run as text
   ("<status> <relative time>" or "no runs yet") and Manage (FR2, EC15). The run link comes
   from GitHub or GitLab data, so it is only rendered through safeRunHref (inside lastRunView). A GitLab row also
   shows its host, "GitLab CI" and the last run's merge request as `!<n>` linked to pr_url (GitLab C FR45). */
"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Badge, Button, Dropdown } from "@devdigest/ui";
import type { CiRepoEntry } from "@devdigest/shared";
import { installedAgents, isGitLabEntry, lastRunMergeRequest, lastRunView, manageHref } from "../../helpers";
import { s } from "./styles";

export function RepoRow({ entry, now }: { entry: CiRepoEntry; now?: number }) {
  const t = useTranslations("integrationsGithubAction.card");
  const run = lastRunView(entry.last_run, now);
  const mr = lastRunMergeRequest(entry);
  const runText = run ? t("lastRun", { status: t(`status.${run.statusKey}`), time: run.time }) : t("noRuns");

  return (
    <li style={s.row}>
      <span className="mono" style={s.repo} title={entry.repo}>
        {entry.repo}
      </span>
      {isGitLabEntry(entry) && (
        <span style={s.target}>
          <Badge>{t("targetGitlab")}</Badge>
          {entry.host && (
            <span className="mono" style={s.host} title={entry.host}>
              {entry.host}
            </span>
          )}
        </span>
      )}
      {mr && (
        <span className="mono" style={s.mr}>
          {mr.href ? (
            <a
              href={mr.href}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={t("mergeRequest", { ref: mr.ref })}
              style={s.runLink}>
              {mr.ref}
            </a>
          ) : (
            mr.ref
          )}
        </span>
      )}
      <span style={s.run} title={run?.absolute ?? runText}>
        {run?.href ? (
          <a href={run.href} target="_blank" rel="noopener noreferrer" style={s.runLink}>
            {runText}
          </a>
        ) : (
          runText
        )}
      </span>
      <Manage entry={entry} />
    </li>
  );
}

/** One installed agent → a link to its CI tab; several → a menu of their names. */
function Manage({ entry }: { entry: CiRepoEntry }) {
  const t = useTranslations("integrationsGithubAction.card");
  const router = useRouter();
  const agents = installedAgents(entry.installations);
  const first = agents[0];
  if (!first) return null;

  if (agents.length === 1) {
    return (
      <Link href={manageHref(first.id)} aria-label={t("manageRepo", { repo: entry.repo })} style={s.manageLink}>
        {t("manage")}
      </Link>
    );
  }
  return (
    <Dropdown
      align="right"
      trigger={
        <Button
          type="button"
          kind="secondary"
          size="sm"
          iconRight="ChevronDown"
          aria-haspopup="menu"
          aria-label={t("manageRepo", { repo: entry.repo })}>
          {t("manage")}
        </Button>
      }
      items={agents.map((a) => ({ label: a.name, onClick: () => router.push(manageHref(a.id)) }))}
    />
  );
}
