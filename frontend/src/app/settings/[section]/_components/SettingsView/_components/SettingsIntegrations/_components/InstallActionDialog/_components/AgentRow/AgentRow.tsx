/* AgentRow — one agent of the install dialog: a checkbox (disabled with the reason when the
   agent cannot go to CI) and, once a run started, its progress: waiting, spinner, the PR link with
   the secrets and variables the workflow needs, or the error message (FR3, EC13, FR32). The PR
   link arrives already checked by safeRunHref. For a GitLab repository the words are merge-request and
   CI/CD-variable ones and the next steps of GitLab C FR43 follow (FR45). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { GITLAB_TOKEN_VARIABLE, type AgentOffer, type AgentRun } from "../../helpers";
import { s } from "./styles";

export function AgentRow({
  offer,
  checked,
  gitlab,
  locked,
  run,
  onToggle,
}: {
  offer: AgentOffer;
  checked: boolean;
  /** The selected repository is a GitLab one. */
  gitlab: boolean;
  /** The selection cannot change (a run is going or has started). */
  locked: boolean;
  run?: AgentRun;
  onToggle: () => void;
}) {
  const t = useTranslations("integrationsGithubAction.dialog");
  const { agent, block } = offer;
  const reasonId = React.useId();
  const disabled = block !== null || locked;

  return (
    <li style={s.row}>
      <div style={s.line}>
        <label style={s.label(disabled)}>
          <input
            type="checkbox"
            checked={checked}
            disabled={disabled}
            aria-describedby={block ? reasonId : undefined}
            onChange={onToggle}
          />
          <span style={s.name} title={agent.name}>
            {agent.name}
          </span>
        </label>
      </div>
      {block && (
        <div id={reasonId} style={s.reason}>
          {t(`${gitlab ? "reasonGitlab" : "reason"}.${block}`, { provider: agent.provider })}
        </div>
      )}
      {run && <Progress run={run} gitlab={gitlab} />}
    </li>
  );
}

/** "Label: NAME, NAME" — the names are data (secret / variable names), shown as code. */
function NameList({ label, names }: { label: string; names: readonly string[] }) {
  if (names.length === 0) return null;
  return (
    <div style={s.names}>
      <span>{label}</span>
      {names.map((name) => (
        <code key={name} style={s.code}>
          {name}
        </code>
      ))}
    </div>
  );
}

/** GitLab next steps (FR43): the token hint when the token is a required secret, and the include to add when the
 *  project already has a root config. */
function GitlabSteps({ run }: { run: Extract<AgentRun, { status: "done" }> }) {
  const t = useTranslations("integrationsGithubAction.dialog.progressGitlab");
  const tokenNeeded = run.secrets.includes(GITLAB_TOKEN_VARIABLE);
  const include = run.gitlab && run.gitlab.root_config !== "create" ? run.gitlab : null;
  return (
    <>
      {tokenNeeded && <div style={s.hint}>{t("tokenHint", { name: GITLAB_TOKEN_VARIABLE })}</div>}
      {include && (
        <div style={s.hint}>
          <div>{t("includeHint", { path: include.config_path })}</div>
          <pre style={s.snippet}>{include.include_snippet}</pre>
        </div>
      )}
    </>
  );
}

function Progress({ run, gitlab }: { run: AgentRun; gitlab: boolean }) {
  const t = useTranslations("integrationsGithubAction.dialog.progress");
  const tg = useTranslations("integrationsGithubAction.dialog.progressGitlab");
  switch (run.status) {
    case "queued":
      return <div style={s.progress}>{t("queued")}</div>;
    case "running":
      return (
        <div style={s.progress} role="status">
          <Icon.RefreshCw size={13} style={s.spinner} />
          {t("running")}
        </div>
      );
    case "done":
      return (
        <>
          <div style={s.progress}>
            <Icon.Check size={13} style={s.ok} />
            <span style={s.ok}>{run.prUrl ? (gitlab ? tg("done") : t("done")) : t("doneNoLink")}</span>
            {run.prUrl && (
              <a href={run.prUrl} target="_blank" rel="noopener noreferrer" style={s.prLink}>
                {gitlab ? tg("viewPr") : t("viewPr")}
              </a>
            )}
          </div>
          <NameList label={gitlab ? tg("secrets") : t("secrets")} names={run.secrets} />
          <NameList label={gitlab ? tg("variables") : t("variables")} names={run.variables} />
          {gitlab && <GitlabSteps run={run} />}
        </>
      );
    case "failed":
      return (
        <div role="alert" style={{ ...s.progress, ...s.fail }}>
          {run.message ? t("failed", { message: run.message }) : t("failedUnknown")}
        </div>
      );
  }
}
