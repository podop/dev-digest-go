import type { ContextDocType } from "@devdigest/shared";

/** Badge colours per document type (specs blue, docs green, insights amber). */
export const DOC_TYPE_STYLE: Record<ContextDocType, { color: string; bg: string }> = {
  specs: { color: "var(--accent-text)", bg: "var(--accent-bg)" },
  docs: { color: "var(--ok)", bg: "var(--ok-bg)" },
  insights: { color: "var(--warn)", bg: "var(--warn-bg)" },
};
