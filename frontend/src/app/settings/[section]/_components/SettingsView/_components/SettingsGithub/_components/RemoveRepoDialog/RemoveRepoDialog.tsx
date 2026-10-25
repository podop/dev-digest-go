/* RemoveRepoDialog — confirm before DELETE /repos/:id (FR19). The vendored Modal provides
   role="dialog"; Escape, the focus trap and focus return come from useDialogFocus. The button
   stays disabled while the request runs, so a double click sends one request (EC17). */
"use client";

import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useDeleteRepo } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { s } from "./styles";

export function RemoveRepoDialog({ repo, onClose }: { repo: Repo; onClose: () => void }) {
  const t = useTranslations("githubIntegration.removeDialog");
  const del = useDeleteRepo();
  const ref = useDialogFocus(onClose);

  const confirm = async () => {
    if (del.isPending) return;
    try {
      await del.mutateAsync(repo.id);
      onClose();
    } catch {
      // The error shows in the dialog; it stays open so the user can retry or cancel.
    }
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={460}
        title={t("title", { repo: repo.full_name })}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button kind="ghost" onClick={onClose} disabled={del.isPending}>
              {t("cancel")}
            </Button>
            <Button kind="danger" onClick={() => void confirm()} disabled={del.isPending}>
              {del.isPending ? t("removing") : t("confirm")}
            </Button>
          </div>
        }
      >
        <div style={s.body}>
          {t("body")}
          {del.isError && (
            <div role="alert" style={s.error}>
              {del.error instanceof ApiError ? del.error.message : t("error")}
            </div>
          )}
        </div>
      </Modal>
    </div>
  );
}
