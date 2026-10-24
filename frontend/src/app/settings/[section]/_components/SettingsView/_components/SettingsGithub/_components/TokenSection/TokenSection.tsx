"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { FormField, Icon } from "@devdigest/ui";
import { KEY_PAGE_URLS, KeyField } from "@/components/key-field";
import { useSecretsStatus } from "@/lib/hooks";
import type { ConnTestResult } from "@/lib/types";
import { lacksRepoScope, scopesText } from "../../helpers";
import { s } from "./styles";

/** What the last successful test told us about the token; a failed test clears it. */
type TokenInfo = { login: string | null; scopes: readonly string[] | null };

export function TokenSection() {
  const t = useTranslations("githubIntegration");
  const { data: status } = useSecretsStatus();
  const [info, setInfo] = React.useState<TokenInfo | null>(null);

  const onResult = React.useCallback((r: ConnTestResult) => {
    // `scopes` is null/absent for fine-grained tokens (FR16); failures carry no extras (A14).
    setInfo(r.ok ? { login: r.login ?? null, scopes: r.scopes ?? null } : null);
  }, []);

  return (
    <>
      <KeyField
        provider="github"
        label={t("token.label")}
        hint={t("token.hint")}
        docsHref={KEY_PAGE_URLS.github}
        configured={status?.github}
        autoTest
        onResult={onResult}
      />
      {info && (
        <p style={s.line}>
          {info.scopes === null
            ? t("scopes.fineGrained")
            : info.scopes.length === 0
              ? t("scopes.none")
              : t("scopes.list", { scopes: scopesText(info.scopes) })}
        </p>
      )}
      {info && lacksRepoScope(info.scopes) && (
        <div role="status" style={s.warn}>
          <Icon.AlertTriangle size={13} />
          {t("scopes.missingRepo")}
        </div>
      )}
      <FormField label={t("identity.label")}>
        <div style={s.identity}>
          {info?.login ? t("identity.login", { login: info.login }) : t("identity.owner")}
        </div>
      </FormField>
    </>
  );
}
