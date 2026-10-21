/* BlastGraph — lightweight SVG of the blast radius: changed symbol → callers →
   endpoints / crons in three columns, with a legend. No graph library. Dashed
   lines mark what the API only knows per symbol (endpoints, crons). */
"use client";

import { useTranslations } from "next-intl";
import type { DownstreamImpact } from "@devdigest/shared";
import { graphLayout, NODE_HEIGHT, NODE_WIDTH, type GraphNodeKind } from "./helpers";
import { NODE_COLORS, s } from "./styles";

export interface BlastGraphProps {
  downstream: DownstreamImpact[];
}

const LEGEND: readonly GraphNodeKind[] = ["symbol", "caller", "endpoint", "cron"];

export function BlastGraph({ downstream }: BlastGraphProps) {
  const t = useTranslations("blast");
  const layout = graphLayout(downstream);
  if (layout.nodes.length === 0) return <p style={s.muted}>{t("graph.empty")}</p>;

  const byId = new Map(layout.nodes.map((n) => [n.id, n]));

  return (
    <div style={s.wrap}>
      <svg
        role="img"
        aria-label={t("graph.ariaLabel")}
        viewBox={`0 0 ${layout.width} ${layout.height}`}
        width={layout.width}
        style={s.svg}
      >
        {layout.edges.map((e) => {
          const from = byId.get(e.from);
          const to = byId.get(e.to);
          if (!from || !to) return null;
          const x1 = from.x + NODE_WIDTH;
          const y1 = from.y + NODE_HEIGHT / 2;
          const x2 = to.x;
          const y2 = to.y + NODE_HEIGHT / 2;
          const mid = (x1 + x2) / 2;
          return (
            <path
              key={e.id}
              d={`M${x1},${y1} C${mid},${y1} ${mid},${y2} ${x2},${y2}`}
              fill="none"
              stroke="var(--border-strong)"
              strokeWidth={1.2}
              strokeDasharray={e.dashed ? "4 3" : undefined}
            />
          );
        })}
        {layout.nodes.map((n) => (
          <g key={n.id} transform={`translate(${n.x},${n.y})`}>
            <title>{n.title}</title>
            <rect
              width={NODE_WIDTH}
              height={NODE_HEIGHT}
              rx={5}
              fill={NODE_COLORS[n.kind].fill}
              stroke={NODE_COLORS[n.kind].stroke}
            />
            <text x={10} y={NODE_HEIGHT / 2 + 4} fontSize={12} fill="var(--text-primary)" className="mono">
              {n.label}
            </text>
          </g>
        ))}
      </svg>
      {layout.hiddenSymbols > 0 && <p style={s.muted}>{t("graph.moreSymbols", { count: layout.hiddenSymbols })}</p>}
      <ul style={s.legend} aria-label={t("graph.legend.title")}>
        {LEGEND.map((kind) => (
          <li key={kind} style={s.legendItem}>
            <span style={s.swatch(NODE_COLORS[kind].fill, NODE_COLORS[kind].stroke)} />
            {t(`graph.legend.${kind}`)}
          </li>
        ))}
      </ul>
    </div>
  );
}
