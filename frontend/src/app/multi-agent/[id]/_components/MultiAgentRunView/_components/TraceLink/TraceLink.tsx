/* TraceLink — the mono "View trace" text button of a column footer and the tab summary
   (prototype MonoLink: 12px, accent + underline on hover). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { s } from "./styles";

export function TraceLink({ onClick }: { onClick: () => void }) {
  const t = useTranslations("runs");
  const [hover, setHover] = React.useState(false);
  return (
    <button
      type="button"
      className="mono"
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onFocus={() => setHover(true)}
      onBlur={() => setHover(false)}
      style={s.link(hover)}
    >
      {t("viewTrace")}
    </button>
  );
}
