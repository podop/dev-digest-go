"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, FormField, TextInput } from "@devdigest/ui";
import { copyText } from "@/lib/clipboard";
import { useWorkspace } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { COPIED_MS } from "./constants";
import { s } from "./styles";

/** Workspace → "Data directory" (FR4): the absolute data dir, read-only, mono, with a copy button. */
export function DataDirField() {
  const t = useTranslations("settingsWorkspace");
  const toast = useToast();
  const { data: workspace } = useWorkspace();
  const [copied, setCopied] = React.useState(false);

  React.useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);

  const dataDir = workspace?.dataDir ?? "";
  const copy = async () => {
    if (await copyText(dataDir)) setCopied(true);
    else toast.error(t("dataDir.copyFailed"));
  };

  return (
    <FormField label={t("dataDir.label")} hint={t("dataDir.hint")}>
      <div style={s.row}>
        <div style={s.input}>
          <TextInput value={dataDir} mono readOnly aria-label={t("dataDir.inputAria")} />
        </div>
        {copied && (
          <span role="status" style={s.copied}>
            {t("dataDir.copied")}
          </span>
        )}
        <Button kind="secondary" size="md" icon="Copy" type="button" disabled={dataDir === ""} onClick={() => void copy()}>
          {t("dataDir.copy")}
        </Button>
      </div>
    </FormField>
  );
}
