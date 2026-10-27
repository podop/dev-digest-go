/* TestResult — one GitLab connection test (FR34): "Connected as @user", scopes, expiry, version, token access and
   one warning box per returned warning (the server's FR11 copy). A failed test is an alert with its message.
   Every state has text, never colour alone (NFR12). */
"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { ConnTestResult } from "@/lib/types";
import { scopesText } from "../../helpers";
import { s } from "./styles";

export function TestResult({ result }: { result: ConnTestResult }) {
  const t = useTranslations("gitlabIntegration.result");

  if (!result.ok) {
    return (
      <div role="alert" style={s.crit}>
        <Icon.XCircle size={15} style={s.icon("crit")} aria-hidden />
        {result.message}
      </div>
    );
  }

  const scopes = result.scopes;
  return (
    <div style={s.wrap}>
      <div role="status" style={s.ok}>
        <Icon.CheckCircle size={13} aria-hidden />
        {t("connectedAs", { login: result.login ?? "" })}
      </div>
      <ul style={s.facts}>
        <li>{scopes ? t("scopes", { scopes: scopesText(scopes) }) : t("scopesUnknown")}</li>
        <li>{result.expires_at ? t("expires", { date: result.expires_at }) : t("noExpiry")}</li>
        <li>{result.instance_version ? t("version", { version: result.instance_version }) : t("versionUnknown")}</li>
        {result.token_access && <li>{result.token_access === "full" ? t("accessFull") : t("accessReadOnly")}</li>}
      </ul>
      {(result.warnings ?? []).map((w) => (
        <div key={w.code} role="status" style={s.warn}>
          <Icon.AlertTriangle size={15} style={s.icon("warn")} aria-hidden />
          {w.message}
        </div>
      ))}
    </div>
  );
}
