/* Notice — an inline banner above the cards (stale tour, provider not configured): a message and
   one action. */
"use client";

import type React from "react";
import { s } from "./styles";

export type NoticeTone = "warn" | "crit";

export function Notice({
  tone,
  label,
  children,
  action,
}: {
  tone: NoticeTone;
  /** Accessible name of the banner region. */
  label: string;
  children: React.ReactNode;
  action: React.ReactNode;
}) {
  return (
    <section aria-label={label} style={{ ...s.box, ...s[tone] }}>
      <div style={s.text}>{children}</div>
      {action}
    </section>
  );
}
