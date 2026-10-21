import type { OnboardingNodeKind } from "@devdigest/shared";

/** Fixed diagram geometry (SVG user units). */
export const BOX_W = 150;
export const BOX_H = 36;
export const GAP_X = 70;
export const GAP_Y = 22;
export const PAD = 18;
/** Label characters that fit a box in the 12px mono font. */
export const LABEL_CHARS = 18;
/** Fewer usable nodes than this hide the diagram. */
export const MIN_NODES = 2;

/** Box outline per node kind (design tokens, so both themes work). */
export const KIND_COLOR: Record<OnboardingNodeKind, string> = {
  entry: "var(--accent)",
  module: "var(--warn)",
  store: "var(--ok)",
  external: "var(--border-strong)",
};
