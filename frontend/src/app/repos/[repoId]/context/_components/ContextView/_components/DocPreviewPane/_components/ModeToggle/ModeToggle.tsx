/* ModeToggle — the Preview | Edit switch in the pane header. Edit is disabled
   (with a hint) for documents that are not editable — the repository's own files. */
"use client";

import { useTranslations } from "next-intl";
import type { ContextMode } from "../../../../helpers";
import { s } from "./styles";

const MODES: readonly ContextMode[] = ["preview", "edit"];

export function ModeToggle({
  mode,
  canEdit,
  onChange,
}: {
  mode: ContextMode;
  canEdit: boolean;
  onChange: (mode: ContextMode) => void;
}) {
  const t = useTranslations("context");
  return (
    <div role="group" aria-label={t("mode.label")} style={s.group}>
      {MODES.map((m) => {
        const disabled = m === "edit" && !canEdit;
        return (
          <span key={m} title={disabled ? t("mode.readOnlyHint") : undefined}>
            <button
              type="button"
              aria-pressed={mode === m}
              disabled={disabled}
              onClick={() => onChange(m)}
              style={{ ...s.btn, ...(mode === m ? s.btnOn : null), ...(disabled ? s.btnOff : null) }}
            >
              {t(`mode.${m}`)}
            </button>
          </span>
        );
      })}
    </div>
  );
}
