/* DiffPreview — the pasted diff rendered by the app's diff viewer, with the expectation's
   lines highlighted (FR10). The viewer draws every line as text, never as HTML (NFR1). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { DiffViewer, type DiffHighlight } from "@/components/diff-viewer";
import type { DiffAnalysis, PreviewStatus } from "../../helpers";
import { s } from "../../styles";

export function DiffPreview({ status, analysis, highlight }: { status: PreviewStatus; analysis: DiffAnalysis; highlight: DiffHighlight | null }) {
  const t = useTranslations("eval.caseEditor");
  return (
    <section aria-label={t("preview")} style={s.section}>
      <h3 style={s.heading}>{t("preview")}</h3>
      {status === "ready" ? (
        <div style={s.preview}>
          <DiffViewer files={analysis.files} highlight={highlight} />
        </div>
      ) : (
        <p style={s.hint}>{t(`previewState.${status}`)}</p>
      )}
    </section>
  );
}
