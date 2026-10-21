/* WizardErrorBox — an API error inside the wizard: its code, the server's message and, for
   `workflow_insecure`, the failed rule. The message names the token scopes for
   `github_permission_denied`. Plain text only. */
"use client";

import { useTranslations } from "next-intl";
import type { WizardError } from "../../helpers";
import { s } from "../../styles";

export function WizardErrorBox({ error }: { error: WizardError }) {
  const t = useTranslations("ci.exportWizard");
  return (
    <div role="alert" style={s.error}>
      {error.code && (
        <span className="mono" style={s.errorCode}>
          {error.code}
        </span>
      )}
      {error.message}
      {error.rule && <span style={s.errorRule}>{t("failedRule", { rule: error.rule })}</span>}
    </div>
  );
}
