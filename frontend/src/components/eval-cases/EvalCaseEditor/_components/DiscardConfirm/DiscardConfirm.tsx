/* DiscardConfirm — "Discard changes?" (FR14). Rendered next to the editor, never inside it. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal } from "@devdigest/ui";
import { s } from "../../styles";

export function DiscardConfirm({ onDiscard, onKeepEditing }: { onDiscard: () => void; onKeepEditing: () => void }) {
  const t = useTranslations("eval.caseEditor.discard");
  return (
    <Modal
      width={440}
      title={t("title")}
      onClose={onKeepEditing}
      footer={
        <div style={s.confirmFooter}>
          <Button kind="ghost" onClick={onKeepEditing}>
            {t("keep")}
          </Button>
          <Button kind="danger" onClick={onDiscard}>
            {t("confirm")}
          </Button>
        </div>
      }
    >
      <p style={s.confirmBody}>{t("body")}</p>
    </Modal>
  );
}
