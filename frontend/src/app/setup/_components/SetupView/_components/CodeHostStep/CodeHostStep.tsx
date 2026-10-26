"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { ConnTestProvider, ConnTestResult, GitLabConnection, SecretsStatus } from "@/lib/types";
import { CODE_HOSTS, type CodeHost } from "../../constants";
import { gitlabTokenCount, initialHost } from "../../helpers";
import { StepFrame } from "../StepFrame";
import { GithubPanel } from "./_components/GithubPanel";
import { GitlabPanel } from "./_components/GitlabPanel";
import { s } from "./styles";

/** Step 2 (FR38): connect a code host — a GitHub token (FR24) or a GitLab connection. Continue needs a
 *  passing test of this view on either host, or a stored GitHub token / GitLab token that no test of this
 *  view has failed on that host. */
export function CodeHostStep({
  secrets,
  gitlabConnections,
  onTested,
  onBack,
  onContinue,
}: {
  secrets: SecretsStatus;
  gitlabConnections: readonly GitLabConnection[];
  onTested: (provider: ConnTestProvider) => void;
  onBack?: () => void;
  onContinue?: () => void;
}) {
  const t = useTranslations("setup");
  const gitlabTokens = gitlabTokenCount(gitlabConnections);
  const [host, setHost] = React.useState<CodeHost>(() => initialHost(secrets, gitlabTokens));
  // Last test outcome of this view, per host; switching host keeps both.
  const [results, setResults] = React.useState<Partial<Record<CodeHost, ConnTestResult>>>({});

  const ready = (results.github ? results.github.ok : secrets.github) || (results.gitlab ? results.gitlab.ok : gitlabTokens > 0);
  const stored = host === "github" ? secrets.github : gitlabTokens > 0;
  const alreadyConnected = stored && !results[host];

  const onResult = (next: ConnTestResult) => {
    const which: CodeHost = next.provider === "gitlab" ? "gitlab" : "github";
    setResults((prev) => ({ ...prev, [which]: next }));
    if (next.ok) onTested(which);
  };

  return (
    <StepFrame
      title={t("codeHost.title")}
      sub={t("codeHost.sub")}
      onBack={onBack}
      primary={{ label: t("actions.continue"), onClick: () => onContinue?.(), disabled: !ready }}
    >
      <fieldset style={s.group}>
        <legend style={s.legend}>{t("codeHost.hostLegend")}</legend>
        <div style={s.options}>
          {CODE_HOSTS.map((h) => (
            <label key={h} style={s.option(h === host)}>
              <input type="radio" name="setup-code-host" value={h} checked={h === host} onChange={() => setHost(h)} />
              {t(`codeHost.hosts.${h}`)}
            </label>
          ))}
        </div>
      </fieldset>
      {host === "github" ? (
        <GithubPanel configured={secrets.github} result={results.github ?? null} onResult={onResult} />
      ) : (
        <GitlabPanel connections={gitlabConnections} result={results.gitlab ?? null} onResult={onResult} />
      )}
      {alreadyConnected && (
        <div style={s.connected}>
          <Icon.CheckCircle size={14} />
          {t("alreadyConnected")}
        </div>
      )}
    </StepFrame>
  );
}
