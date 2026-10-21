/* DeleteFileModal — confirm deleting a store file; names how many agents use it
   (their attachment turns `missing`). Rendered as a sibling of the clickable row. */
"use client";

import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";
import type { ContextDoc } from "@devdigest/shared";
import { useDeleteContextFile } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { s } from "./styles";

export function DeleteFileModal({
  repoId,
  doc,
  onClose,
  onDeleted,
}: {
  repoId: string;
  doc: ContextDoc;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const t = useTranslations("context");
  const toast = useToast();
  const del = useDeleteContextFile(repoId);

  const confirm = async () => {
    try {
      await del.mutateAsync(doc.path);
    } catch {
      return; // toasted by the global mutation handler; the dialog stays open
    }
    toast.success(t("store.deleteDialog.deleted", { name: doc.name }));
    onDeleted();
  };

  return (
    <Modal
      width={480}
      title={t("store.deleteDialog.title")}
      onClose={onClose}
      footer={
        <div style={s.footer}>
          <Button kind="ghost" onClick={onClose}>
            {t("store.deleteDialog.cancel")}
          </Button>
          <Button kind="danger" icon="Trash" onClick={() => void confirm()} disabled={del.isPending}>
            {del.isPending ? t("store.deleteDialog.deleting") : t("store.deleteDialog.confirm")}
          </Button>
        </div>
      }
    >
      <div style={s.body}>
        <p className="mono" style={s.path}>
          {t("store.deleteDialog.body", { path: doc.path })}
        </p>
        {doc.used_by > 0 && <p style={s.warn}>{t("store.deleteDialog.usedBy", { count: doc.used_by })}</p>}
      </div>
    </Modal>
  );
}
