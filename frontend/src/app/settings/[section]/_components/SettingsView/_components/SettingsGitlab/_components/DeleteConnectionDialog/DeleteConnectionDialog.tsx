/* DeleteConnectionDialog — confirm before DELETE /settings/gitlab-connections/:id (FR36). It names the connection
   and, from its repo_count, how many repositories become "not connected". The request runs once (the button is
   disabled meanwhile); a failure shows the error here and the dialog (and the row) stay. Escape, the focus trap and
   focus return come from useDialogFocus. */
"use client";

import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useDeleteGitLabConnection } from "@/lib/hooks";
import type { GitLabConnection } from "@/lib/types";
import { s } from "./styles";

export function DeleteConnectionDialog({ connection, onClose }: { connection: GitLabConnection; onClose: () => void }) {
  const t = useTranslations("gitlabIntegration.deleteDialog");
  const del = useDeleteGitLabConnection();
  const ref = useDialogFocus(onClose);

  const confirm = async () => {
    if (del.isPending) return;
    try {
      await del.mutateAsync(connection.id);
      onClose();
    } catch {
      // The error shows in the dialog; it stays open so the user can retry or cancel.
    }
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={480}
        title={t("title", { name: connection.name })}
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
        <div style={s.body}>
          {connection.repo_count > 0
            ? t("bodyRepos", { count: connection.repo_count, host: connection.host })
            : t("bodyNone")}
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
