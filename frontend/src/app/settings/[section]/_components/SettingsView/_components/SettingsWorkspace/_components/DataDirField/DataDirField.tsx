"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { Field, TextField } from "@/components/settings-kit";
import { copyText } from "@/lib/clipboard";
import { useWorkspace } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { COPIED_MS } from "./constants";
import { s } from "./styles";

/** Workspace → "Data directory" (FR4): the absolute data dir, read-only, mono, with a copy action inside the box. */
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
    <Field label={t("dataDir.label")} hint={t("dataDir.hint")}>
      <TextField
        value={dataDir}
        mono
        readOnly
        aria-label={t("dataDir.inputAria")}
        suffix={
          <>
            {copied && (
              <span role="status" style={s.copied}>
                {t("dataDir.copied")}
              </span>
            )}
            <button type="button" style={s.copy} disabled={dataDir === ""} onClick={() => void copy()}>
              <Icon.Copy size={13} aria-hidden />
              {t("dataDir.copy")}
            </button>
          </>
        }
      />
    </Field>
  );
}
