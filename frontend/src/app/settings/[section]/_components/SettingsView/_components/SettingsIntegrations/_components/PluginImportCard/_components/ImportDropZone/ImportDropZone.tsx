/* ImportDropZone — drop a file or "browse files…" (hidden file input behind a focusable button), laid out as the
   prototype's dashed drop zone: Upload icon, then one line of text that ends in the browse link-button.
   It only hands the first file to `onFile`; the type and size checks belong to the card. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { PLUGIN_ACCEPT } from "../../constants";
import { s } from "./styles";

export function ImportDropZone({ checking, onFile }: { checking: boolean; onFile: (file: File) => void }) {
  const t = useTranslations("integrationsImport.zone");
  const inputRef = React.useRef<HTMLInputElement>(null);
  const [over, setOver] = React.useState(false);

  const onDrop = (e: React.DragEvent<HTMLFieldSetElement>) => {
    e.preventDefault();
    setOver(false);
    const file = e.dataTransfer.files[0];
    if (file && !checking) onFile(file);
  };

  const onPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    // Clear the input so choosing the same file again fires a change.
    e.target.value = "";
    if (file && !checking) onFile(file);
  };

  return (
    <fieldset
      aria-label={t("label")}
      aria-busy={checking}
      style={over && !checking ? { ...s.zone, ...s.over } : s.zone}
      onDragOver={(e) => {
        e.preventDefault();
        if (!checking) setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={onDrop}
    >
      {checking ? (
        <div role="status" style={s.checking}>
          {t("checking")}
        </div>
      ) : (
        <>
          <Icon.Upload size={22} style={s.icon} aria-hidden />
          <div style={s.prompt}>
            <span>
              {t.rich("prompt", {
                mono: (chunks) => <span className="mono">{chunks}</span>,
              })}
            </span>{" "}
            <button type="button" data-browse style={s.browse} onClick={() => inputRef.current?.click()}>
              {t("browse")}
            </button>
          </div>
        </>
      )}
      <input
        ref={inputRef}
        type="file"
        accept={PLUGIN_ACCEPT}
        aria-label={t("fileInputAria")}
        tabIndex={-1}
        hidden
        onChange={onPick}
      />
    </fieldset>
  );
}
