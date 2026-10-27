import type { CSSProperties } from "react";

export type Tone = "ok" | "muted" | "warn";

const COLOR: Record<Tone, string> = { ok: "var(--ok)", muted: "var(--text-muted)", warn: "var(--warn)" };
const BOX_BG: Record<Tone, string> = { ok: "var(--ok-bg)", muted: "var(--bg-surface)", warn: "var(--warn-bg)" };
const BOX_BORDER: Record<Tone, string> = {
  ok: "rgba(16,185,129,0.25)",
  muted: "var(--border)",
  warn: "rgba(245,158,11,0.3)",
};

/** Co-located styles for AutoReviewStatus — the prototype's tinted status box ("Active · last poll …"). */
export const s = {
  line: (tone: Tone): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 10,
    padding: "11px 14px",
    borderRadius: 8,
    background: BOX_BG[tone],
    border: `1px solid ${BOX_BORDER[tone]}`,
    marginBottom: 18,
    fontSize: 12.5,
    color: "var(--text-secondary)",
  }),
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  strong: { color: "var(--text-primary)" } satisfies CSSProperties,
  link: { color: "var(--accent-text)", fontWeight: 600 } satisfies CSSProperties,
  /** Green pulsing dot while active, static otherwise. */
  dot: (tone: Tone): CSSProperties => ({
    width: 7,
    height: 7,
    borderRadius: 99,
    flexShrink: 0,
    background: COLOR[tone],
    animation: tone === "ok" ? "ddpulse 2s infinite" : "none",
  }),
} as const;
