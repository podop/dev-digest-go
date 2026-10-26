/* DeleteCustomProviderDialog — confirm before DELETE /settings/custom-providers/:id (FR23). A 409
   `provider_in_use` lists who uses the provider ("Used by:" agents, features, Default model) and keeps the
   dialog (and the row) in place. The button stays disabled while the request runs: one click, one DELETE. */
"use client";

import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useDeleteCustomProvider } from "@/lib/hooks";
import { CUSTOM_PROVIDER_ERROR_CODES } from "@/lib/providers/custom";
import type { CustomProvider } from "@/lib/types";
// Zod-free subpath: the barrel would pull zod + every schema into /settings.
import { FEATURE_MODELS } from "@devdigest/shared/constants/feature-models";
import { inUseUsers } from "./helpers";
import { s } from "./styles";

const FEATURE_LABELS: ReadonlyMap<string, string> = new Map(FEATURE_MODELS.map((f) => [f.id, f.label]));

export function DeleteCustomProviderDialog({ provider, onClose }: { provider: CustomProvider; onClose: () => void }) {
  const t = useTranslations("customProviders.delete");
  const del = useDeleteCustomProvider();
  const ref = useDialogFocus(onClose);

  const confirm = async () => {
    if (del.isPending) return;
    try {
      await del.mutateAsync(provider.id);
      onClose();
    } catch {
      // The reason shows in the dialog; it stays open so the user can read it or cancel.
    }
  };

  const err = del.error;
  const users = err instanceof ApiError && err.code === CUSTOM_PROVIDER_ERROR_CODES.inUse ? inUseUsers(err.details) : null;
  const usedBy = users
    ? [
        ...users.agents.map((a) => ({ key: `agent:${a.id}`, label: a.name })),
        ...users.features.map((id) => ({ key: `feature:${id}`, label: FEATURE_LABELS.get(id) ?? id })),
      ]
    : [];

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={480}
        title={t("title", { name: provider.name })}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={onClose} disabled={del.isPending}>
              {t("cancel")}
            </Button>
            <Button type="button" kind="danger" onClick={() => void confirm()} disabled={del.isPending}>
              {del.isPending ? t("deleting") : t("confirm")}
            </Button>
          </div>
        }
      >
        <div style={s.dialogBody}>
          {t("body")}
          {users && (
            <div role="alert" style={s.usedBy}>
              <div style={s.usedByTitle}>{t("usedBy")}</div>
              <ul style={s.usedByList}>
                {usedBy.map((u) => (
                  <li key={u.key}>{u.label}</li>
                ))}
                {users.defaultModel && <li>{t("defaultModel")}</li>}
              </ul>
            </div>
          )}
          {err && !users && (
            <div role="alert" style={s.error}>
              {err instanceof ApiError && err.message ? err.message : t("failed")}
            </div>
          )}
        </div>
      </Modal>
    </div>
  );
}
