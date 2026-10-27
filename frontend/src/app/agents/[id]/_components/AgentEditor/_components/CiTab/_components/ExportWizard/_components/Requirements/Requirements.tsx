/* Requirements — what the workflow needs in the repository settings, read from the export
   response (FR31): the Actions secrets, `GITHUB_TOKEN` (provided by Actions) and the variables.
   For GitLab (GitLab C FR43) the same lists are CI/CD variables — masked, not protected — with the
   posting-token hint for `DEVDIGEST_GITLAB_TOKEN` and no `GITHUB_TOKEN`.
   For a custom-provider agent the base URL is the variable's value, with a copy button, and a
   loopback base URL gets the warning that names the variable (EC21). Plain text only. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { copyText } from "@/lib/clipboard";
import { useToast } from "@/lib/toast";
import { GITHUB_TOKEN_SECRET, GITLAB_TOKEN_VARIABLE } from "../../constants";
import { isLoopbackBaseUrl, type Requirements as RequirementsData } from "../../helpers";
import { s } from "./styles";

const COPIED_MS = 2000;

export function Requirements({ gitlab, secrets, variables, custom }: RequirementsData) {
  const t = useTranslations("ci.exportWizard");
  const toast = useToast();
  const [copied, setCopied] = React.useState<string | null>(null);
  const baseUrl = custom?.baseUrl ?? null;

  React.useEffect(() => {
    if (copied == null) return;
    const id = setTimeout(() => setCopied(null), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);

  const copy = async (name: string, value: string) => {
    if (await copyText(value)) setCopied(name);
    else toast.error(t("copyFailed"));
  };

  const secretsTitle = gitlab ? t("gitlab.variablesTitle") : t("secretsTitle");
  const variablesTitle = gitlab ? t("gitlab.visibleVariablesTitle") : t("variablesTitle");
  const gitlabSecretHint = (key: string) => (key === GITLAB_TOKEN_VARIABLE ? t("gitlab.tokenHint") : t("gitlab.variableHint"));

  return (
    <div style={s.wrap}>
      <section style={s.group} aria-label={secretsTitle}>
        <h3 style={s.title}>{secretsTitle}</h3>
        <ul style={s.list}>
          {secrets.map((key) => (
            <li key={key} style={s.item}>
              <span className="mono" style={s.key}>
                {key}
              </span>
              <span style={s.muted}>{gitlab ? gitlabSecretHint(key) : t("secretHint")}</span>
            </li>
          ))}
          {!gitlab && (
            <li style={s.item}>
              <span className="mono" style={s.key}>
                {GITHUB_TOKEN_SECRET}
              </span>
              <span style={s.muted}>{t("githubTokenHint")}</span>
            </li>
          )}
        </ul>
      </section>

      {variables.length > 0 && (
        <section style={s.group} aria-label={variablesTitle}>
          <h3 style={s.title}>{variablesTitle}</h3>
          <ul style={s.list}>
            {variables.map((name) => (
              <li key={name} style={s.item}>
                <span className="mono" style={s.key}>
                  {name}
                </span>
                <span style={s.muted}>{gitlab ? t("gitlab.visibleVariableHint") : t("variableHint")}</span>
                {baseUrl && (
                  <>
                    <code className="mono" style={s.value}>
                      {baseUrl}
                    </code>
                    <Button
                      kind="secondary"
                      size="sm"
                      icon="Copy"
                      type="button"
                      aria-label={t("copyValueAria", { name })}
                      onClick={() => void copy(name, baseUrl)}
                    >
                      {t("copyValue")}
                    </Button>
                    {copied === name && (
                      <span role="status" style={s.copied}>
                        {t("copied")}
                      </span>
                    )}
                  </>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {baseUrl && isLoopbackBaseUrl(baseUrl) && (
        <p role="alert" style={s.warning}>
          {variables[0]
            ? t("loopbackWarning", { baseUrl, variable: variables[0] })
            : t("loopbackWarningNoVariable", { baseUrl })}
        </p>
      )}
    </div>
  );
}
