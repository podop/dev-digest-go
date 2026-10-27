/* IncludeSnippet — shown when the project already has a root CI configuration (`keep_existing`,
   `custom_path`): the include the user has to add, a copy button and the two static warnings of
   EC14 and EC15 (SCR3: printed every time, the configuration is never parsed). The path and the
   snippet are server data and render as plain text. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { copyText } from "@/lib/clipboard";
import { useToast } from "@/lib/toast";
import { s } from "./styles";

const COPIED_MS = 2000;

export function IncludeSnippet({ configPath, snippet }: { configPath: string; snippet: string }) {
  const t = useTranslations("ci.exportWizard");
  const toast = useToast();
  const [copied, setCopied] = React.useState(false);

  React.useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);

  const copy = async () => {
    if (await copyText(snippet)) setCopied(true);
    else toast.error(t("copyFailed"));
  };

  return (
    <section style={s.box} aria-label={t("gitlab.includeBoxLabel")}>
      <p style={s.intro}>{t("gitlab.includeBoxIntro", { path: configPath })}</p>
      <pre className="mono" style={s.code}>
        {snippet}
      </pre>
      <div style={s.row}>
        <Button kind="secondary" size="sm" icon="Copy" type="button" aria-label={t("gitlab.copySnippetAria")} onClick={() => void copy()}>
          {t("gitlab.copySnippet")}
        </Button>
        {copied && (
          <span role="status" style={s.copied}>
            {t("copied")}
          </span>
        )}
      </div>
      <ul style={s.warnings}>
        <li>{t("gitlab.warnWorkflowRules")}</li>
        <li>{t("gitlab.warnStagesTest")}</li>
      </ul>
    </section>
  );
}
