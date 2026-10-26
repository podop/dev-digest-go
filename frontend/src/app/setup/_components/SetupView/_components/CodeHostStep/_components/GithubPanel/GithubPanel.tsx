"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KEY_PAGE_URLS, KeyField } from "@/components/key-field";
import type { ConnTestResult } from "@/lib/types";
import { lacksRepoScope } from "../../../../helpers";
import { s } from "./styles";

/** The GitHub half of the code-host step (FR24, kept by FR38): test the token; the login and scopes of a
 *  passing test show below the field. A classic token without `repo` only warns. */
export function GithubPanel({
  configured,
  result,
  onResult,
}: {
  configured: boolean;
  /** The last test of this view (kept by the step while the user looks at GitLab). */
  result: ConnTestResult | null;
  onResult: (result: ConnTestResult) => void;
}) {
  const t = useTranslations("setup");
  const passed = result?.ok ? result : null;

  return (
    <>
      <KeyField
        provider="github"
        label={t("codeHost.github.label")}
        hint={t("storedLocally")}
        docsHref={KEY_PAGE_URLS.github}
        configured={configured}
        onResult={onResult}
      />
      {passed && (
        <div style={s.who}>
          {passed.login && <div style={s.login}>{t("codeHost.connectedAs", { login: passed.login })}</div>}
          <div>{scopesText(passed.scopes, t)}</div>
          {lacksRepoScope(passed.scopes) && (
            <div style={s.warn}>
              <Icon.AlertTriangle size={14} style={s.warnIcon} />
              <span>{t("codeHost.github.missingRepo")}</span>
            </div>
          )}
        </div>
      )}
    </>
  );
}

/** FR17: the scopes joined with " · " (A11), "none" for an empty list, or the fine-grained note. */
function scopesText(scopes: readonly string[] | null | undefined, t: ReturnType<typeof useTranslations>): string {
  if (scopes == null) return t("codeHost.github.scopesNotReported");
  if (scopes.length === 0) return t("codeHost.github.scopesNone");
  return t("codeHost.github.scopes", { scopes: scopes.join(" · ") });
}
