/* ArchitectureCard — body of the "Architecture overview" section: the summary with inline code
   and a boxes-and-arrows diagram coloured by node kind. An invalid diagram (fewer than 2 nodes)
   hides the diagram area and keeps the summary; with neither, "Not enough information". */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { OnboardingArchitecture } from "@devdigest/shared";
import { BOX_H, BOX_W, KIND_COLOR } from "./constants";
import { layoutDiagram, splitInlineCode } from "./helpers";
import { s } from "./styles";

export function ArchitectureCard({ architecture }: { architecture: OnboardingArchitecture }) {
  const t = useTranslations("onboarding");
  const parts = React.useMemo(() => splitInlineCode(architecture.summary), [architecture.summary]);
  const layout = React.useMemo(
    () => layoutDiagram(architecture.nodes, architecture.edges),
    [architecture.nodes, architecture.edges],
  );
  const hasSummary = architecture.summary.trim() !== "";

  if (!hasSummary && !layout) return <p style={s.empty}>{t("empty")}</p>;

  return (
    <div style={s.root}>
      {hasSummary && (
        <p style={s.summary}>
          {parts.map((p, i) =>
            p.code ? (
              <code key={i} className="mono" style={s.code}>
                {p.text}
              </code>
            ) : (
              <React.Fragment key={i}>{p.text}</React.Fragment>
            ),
          )}
        </p>
      )}
      {layout && (
        <div style={s.diagram}>
          <svg
            role="img"
            aria-label={t("arch.diagramLabel")}
            viewBox={`0 0 ${layout.width} ${layout.height}`}
            width={layout.width}
            style={s.svg}
          >
            <defs>
              <marker id="onboarding-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M0 0 L10 5 L0 10 z" style={s.arrowHead} />
              </marker>
            </defs>
            {layout.arrows.map((a) => (
              <line key={a.key} x1={a.x1} y1={a.y1} x2={a.x2} y2={a.y2} markerEnd="url(#onboarding-arrow)" style={s.arrow} />
            ))}
            {layout.boxes.map((b) => (
              <g key={b.id} data-kind={b.kind}>
                <title>{`${b.label} · ${t(`arch.nodeKind.${b.kind}`)}`}</title>
                <rect x={b.x} y={b.y} width={BOX_W} height={BOX_H} rx={7} style={{ ...s.box, stroke: KIND_COLOR[b.kind] }} />
                <text x={b.x + BOX_W / 2} y={b.y + BOX_H / 2} textAnchor="middle" dominantBaseline="central" className="mono" style={s.boxText}>
                  {b.shortLabel}
                </text>
              </g>
            ))}
          </svg>
        </div>
      )}
    </div>
  );
}
