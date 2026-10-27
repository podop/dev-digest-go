/* HealthInfo — the "How health is scored" info button of the Health column (FR21). A tooltip with the
   formula opens on hover and on keyboard focus and closes on blur, mouse-leave and Escape. It is
   positioned from the button (position: fixed, in zoomed CSS px via cssRect), because the table's scroll container would clip it. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { cssRect } from "@/lib/ui-zoom";
import { s } from "./styles";

const TOOLTIP_WIDTH = 280;
/** How far the tooltip reaches past the button's right edge, so it does not run off a narrow viewport. */
const OVERHANG = 40;

export function HealthInfo() {
  const t = useTranslations("leaderboardTable.healthInfo");
  const id = React.useId();
  const [anchor, setAnchor] = React.useState<{ top: number; left: number } | null>(null);

  const show = (button: HTMLElement) => {
    const box = cssRect(button);
    setAnchor({ top: box.bottom + 8, left: Math.max(8, box.right + OVERHANG - TOOLTIP_WIDTH) });
  };
  const hide = () => setAnchor(null);

  return (
    <span style={s.wrap}>
      <button
        type="button"
        aria-label={t("label")}
        aria-describedby={anchor ? id : undefined}
        style={s.button}
        onMouseEnter={(e) => show(e.currentTarget)}
        onMouseLeave={hide}
        onFocus={(e) => show(e.currentTarget)}
        onBlur={hide}
        onKeyDown={(e) => e.key === "Escape" && hide()}
      >
        <Icon.Info size={12} />
      </button>
      {anchor && (
        <div id={id} role="tooltip" style={{ ...s.tooltip, top: anchor.top, left: anchor.left }}>
          <div className="mono" style={s.formula}>
            {t("formula")}
          </div>
          {t("body")}
        </div>
      )}
    </span>
  );
}
