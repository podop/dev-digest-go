/* StoreFileRow — one editable store file: select button (name + folder), a row menu
   with Rename / Delete, and the inline rename field. A rename is checked against the
   path rules before any request; server answers (path_exists, stale…) show inline.
   The delete confirmation renders as a sibling of the clickable row. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Dropdown, Icon, IconBtn } from "@devdigest/ui";
import type { ContextDoc } from "@devdigest/shared";
import { ApiError } from "@/lib/api";
import { useRenameContextFile } from "@/lib/hooks";
import { renameTarget, splitPath, storeRelative } from "../../../../helpers";
import { DeleteFileModal } from "./_components/DeleteFileModal";
import { renameErrorKey, type RenameErrorKey } from "./helpers";
import { s } from "./styles";

export function StoreFileRow({
  repoId,
  doc,
  selected,
  renaming,
  onSelect,
  onStartRename,
  onStopRename,
  onRenamed,
  onDeleted,
}: {
  repoId: string;
  doc: ContextDoc;
  selected: boolean;
  renaming: boolean;
  onSelect: () => void;
  onStartRename: () => void;
  onStopRename: () => void;
  /** Called with the new path once the server renamed the file. */
  onRenamed: (newPath: string) => void;
  onDeleted: () => void;
}) {
  const t = useTranslations("context");
  const rename = useRenameContextFile(repoId);
  const [confirmingDelete, setConfirmingDelete] = React.useState(false);
  const [value, setValue] = React.useState(() => storeRelative(doc.path));
  const [error, setError] = React.useState<RenameErrorKey | null>(null);
  const input = React.useRef<HTMLInputElement>(null);
  const { name, folder } = splitPath(storeRelative(doc.path));

  React.useEffect(() => {
    if (!renaming) return;
    const el = input.current;
    el?.focus();
    // Select the file name, so typing replaces it and keeps the folder.
    el?.setSelectionRange(folder.length, el.value.length - ".md".length);
  }, [renaming, folder.length]);

  const stop = () => {
    setValue(storeRelative(doc.path));
    setError(null);
    onStopRename();
  };

  const commit = async () => {
    const target = renameTarget(value);
    if (target === null) {
      setError("invalidPath");
      return;
    }
    if (target === doc.path) {
      stop();
      return;
    }
    try {
      await rename.mutateAsync({ path: doc.path, new_path: target, base_version: doc.version ?? 0 });
      onRenamed(target);
    } catch (e) {
      // Other failures were already toasted by the global mutation handler.
      setError(e instanceof ApiError ? renameErrorKey(e.code) : null);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      stop();
    }
  };

  return (
    <li>
      <div style={{ ...s.row, ...(selected ? s.rowActive : null) }}>
        {renaming ? (
          <div style={s.rename}>
            <input
              ref={input}
              className="mono"
              aria-label={t("store.renameLabel", { name: doc.name })}
              aria-invalid={error !== null}
              value={value}
              disabled={rename.isPending}
              onChange={(e) => {
                setValue(e.target.value);
                setError(null);
              }}
              onKeyDown={onKeyDown}
              onBlur={() => !rename.isPending && stop()}
              style={s.input}
            />
            {error ? (
              <p role="alert" style={s.error}>
                {t(`store.errors.${error}`)}
              </p>
            ) : (
              <p style={s.hint}>{t("store.renameHint")}</p>
            )}
          </div>
        ) : (
          <>
            <button
              type="button"
              title={doc.path}
              aria-current={selected ? "true" : undefined}
              onClick={onSelect}
              style={{ ...s.select, ...(selected ? s.selectActive : null) }}
            >
              <Icon.FileText size={14} style={selected ? s.iconActive : s.icon} />
              <span className="mono" style={s.name}>
                {folder + name}
              </span>
            </button>
            <span style={s.menu}>
              <Dropdown
                align="right"
                width={160}
                trigger={<IconBtn icon="Menu" label={t("store.actions", { name: doc.name })} />}
                items={[
                  { label: t("store.rename"), icon: "Edit", onClick: onStartRename },
                  { label: t("store.delete"), icon: "Trash", onClick: () => setConfirmingDelete(true) },
                ]}
              />
            </span>
          </>
        )}
      </div>
      {confirmingDelete && (
        <DeleteFileModal
          repoId={repoId}
          doc={doc}
          onClose={() => setConfirmingDelete(false)}
          onDeleted={() => {
            setConfirmingDelete(false);
            onDeleted();
          }}
        />
      )}
    </li>
  );
}
