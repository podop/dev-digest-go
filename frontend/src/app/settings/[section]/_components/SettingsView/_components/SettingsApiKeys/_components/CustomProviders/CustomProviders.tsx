/* CustomProviders — the "Custom providers" block of Settings → API Keys (FR16): the list with its loading,
   empty and error states, "Add custom provider", and the add / edit / delete dialogs. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Skeleton } from "@devdigest/ui";
import { useCustomProviders } from "@/lib/hooks";
import type { CustomProvider } from "@/lib/types";
import { CustomProviderDialog } from "../CustomProviderDialog";
import { CustomProviderRow } from "./CustomProviderRow";
import { DeleteCustomProviderDialog } from "./DeleteCustomProviderDialog";
import { s } from "./styles";

type Dialog = { kind: "add" } | { kind: "edit"; provider: CustomProvider } | { kind: "delete"; provider: CustomProvider };

export function CustomProviders() {
  const t = useTranslations("customProviders");
  const list = useCustomProviders();
  const [dialog, setDialog] = React.useState<Dialog | null>(null);
  const close = () => setDialog(null);

  return (
    <section style={s.wrap} aria-labelledby="custom-providers-title">
      <div style={s.head}>
        <h2 id="custom-providers-title" style={s.title}>
          {t("title")}
        </h2>
        <span style={s.spacer} />
        <Button type="button" kind="secondary" size="sm" icon="Plus" onClick={() => setDialog({ kind: "add" })}>
          {t("add")}
        </Button>
      </div>
      <p style={s.body}>{t("body")}</p>
      {list.isError ? (
        <ErrorState title={t("loadFailedTitle")} body={t("loadFailedBody")} retryLabel={t("retry")} onRetry={() => void list.refetch()} />
      ) : list.isPending ? (
        <div style={s.skeletons} aria-hidden>
          <Skeleton height={72} />
          <Skeleton height={72} />
        </div>
      ) : list.data.length === 0 ? (
        <p style={s.empty}>{t("empty")}</p>
      ) : (
        <ul style={s.list}>
          {list.data.map((p) => (
            <CustomProviderRow
              key={p.id}
              provider={p}
              onEdit={() => setDialog({ kind: "edit", provider: p })}
              onDelete={() => setDialog({ kind: "delete", provider: p })}
            />
          ))}
        </ul>
      )}
      {dialog?.kind === "add" && <CustomProviderDialog onClose={close} />}
      {dialog?.kind === "edit" && <CustomProviderDialog key={dialog.provider.id} provider={dialog.provider} onClose={close} />}
      {dialog?.kind === "delete" && <DeleteCustomProviderDialog provider={dialog.provider} onClose={close} />}
    </section>
  );
}
