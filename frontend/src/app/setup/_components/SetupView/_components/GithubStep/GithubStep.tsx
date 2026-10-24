"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KEY_PAGE_URLS, KeyField } from "@/components/key-field";
import type { ConnTestProvider, ConnTestResult, SecretsStatus } from "@/lib/types";
import { lacksRepoScope } from "../../helpers";
import { StepFrame } from "../StepFrame";
import { s } from "./styles";

/** Step 2 (FR24): test the GitHub token; the login and scopes of a passing test show below the
 *  field. Continue needs a passing test or a stored token that no test of this view has failed.
 *  A classic token without `repo` only warns. */
export function GithubStep({
  secrets,
  onTested,
  onBack,
  onContinue,
}: {
  secrets: SecretsStatus;
  onTested: (provider: ConnTestProvider) => void;
  onBack?: () => void;
  onContinue?: () => void;
}) {
  const t = useTranslations("setup");
  const [result, setResult] = React.useState<ConnTestResult | null>(null);

  const ready = result ? result.ok : secrets.github;
  const alreadyConnected = secrets.github && !result;
  const passed = result?.ok ? result : null;

  const onResult = (next: ConnTestResult) => {
    setResult(next);
    if (next.ok) onTested("github");
  };

  return (
    <StepFrame
      title={t("github.title")}
      sub={t("github.sub")}
      onBack={onBack}
      primary={{ label: t("actions.continue"), onClick: () => onContinue?.(), disabled: !ready }}
    >
      <KeyField
        provider="github"
        label={t("github.label")}
        hint={t("storedLocally")}
        docsHref={KEY_PAGE_URLS.github}
        configured={secrets.github}
        onResult={onResult}
      />
      {alreadyConnected && (
        <div style={s.connected}>
          <Icon.CheckCircle size={14} />
          {t("alreadyConnected")}
        </div>
      )}
      {passed && (
        <div style={s.who}>
          {passed.login && <div style={s.login}>{t("github.connectedAs", { login: passed.login })}</div>}
          <div>{scopesText(passed.scopes, t)}</div>
          {lacksRepoScope(passed.scopes) && (
            <div style={s.warn}>
              <Icon.AlertTriangle size={14} style={s.warnIcon} />
              <span>{t("github.missingRepo")}</span>
            </div>
          )}
        </div>
      )}
    </StepFrame>
  );
}

/** FR17: the scopes joined with " · " (A11), "none" for an empty list, or the fine-grained note. */
function scopesText(scopes: readonly string[] | null | undefined, t: ReturnType<typeof useTranslations>): string {
  if (scopes == null) return t("github.scopesNotReported");
  if (scopes.length === 0) return t("github.scopesNone");
  return t("github.scopes", { scopes: scopes.join(" · ") });
}
