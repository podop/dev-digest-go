/* DeleteCaseConfirm — "Delete eval case?" confirmation (FR22). Rendered by the Evals
   tab next to the list, never inside a row. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";

export function DeleteCaseConfirm({
  name,
  deleting,
  onConfirm,
  onCancel,
}: {
  name: string;
  deleting: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const t = useTranslations("eval.deleteConfirm");
  const tc = useTranslations("common");
  return (
    <Modal
      width={440}
      title={t("title")}
      onClose={onCancel}
      footer={
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          <Button kind="ghost" onClick={onCancel} disabled={deleting}>
            {tc("actions.cancel")}
          </Button>
          <Button kind="danger" icon="Trash" onClick={onConfirm} loading={deleting}>
            {deleting ? t("deleting") : t("confirm")}
          </Button>
        </div>
      }
    >
      <p style={{ margin: 0, padding: "18px 24px", fontSize: 14, lineHeight: 1.5 }}>{t("body", { name })}</p>
    </Modal>
  );
}
