/* NewFolderDialog — asks for a folder name and hands back `<folder>/untitled.md`
   (folders exist only through their files). The name is checked before any request. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal, TextInput } from "@devdigest/ui";
import { newFolderPath } from "../../../../helpers";
import { s } from "./styles";

export function NewFolderDialog({
  busy,
  onSubmit,
  onClose,
}: {
  busy: boolean;
  onSubmit: (path: string) => void;
  onClose: () => void;
}) {
  const t = useTranslations("context");
  const [name, setName] = React.useState("");
  const [touched, setTouched] = React.useState(false);
  const path = newFolderPath(name);
  const invalid = touched && path === null;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (path) onSubmit(path);
  };

  return (
    <Modal
      width={440}
      title={t("store.folderDialog.title")}
      onClose={onClose}
      footer={
        <div style={s.footer}>
          <Button kind="ghost" onClick={onClose}>
            {t("store.folderDialog.cancel")}
          </Button>
          <Button kind="primary" type="submit" form="new-folder-form" disabled={busy}>
            {busy ? t("store.folderDialog.creating") : t("store.folderDialog.create")}
          </Button>
        </div>
      }
    >
      <form id="new-folder-form" onSubmit={submit} style={s.body}>
        <label htmlFor="new-folder-name" style={s.label}>
          {t("store.folderDialog.label")}
        </label>
        <TextInput
          id="new-folder-name"
          mono
          value={name}
          onChange={(v) => {
            setName(v);
            setTouched(true);
          }}
          placeholder={t("store.folderDialog.placeholder")}
          aria-invalid={invalid}
        />
        {invalid ? (
          <p role="alert" style={s.error}>
            {t("store.folderDialog.invalid")}
          </p>
        ) : (
          path && (
            <p className="mono" style={s.hint}>
              {t("store.folderDialog.hint", { path })}
            </p>
          )
        )}
      </form>
    </Modal>
  );
}
