/* CardStats — the stats footer of the agent and skill list cards (mockup: "142 runs  78% accept
   $0.04 avg", "3 agents  71% pull  74% accept"): a divider, then small muted items; an item may
   carry its own colour (accept rate) or render in mono (cost). Presentational. */
import React from "react";
import { s } from "./styles";

export interface CardStat {
  key: string;
  text: React.ReactNode;
  color?: string;
  mono?: boolean;
}

export function CardStats({ items, title }: { items: readonly CardStat[]; title?: string }) {
  return (
    <div style={s.footer} title={title} data-testid="card-stats">
      {items.map((it) => (
        <span key={it.key} className={it.mono ? "mono tnum" : "tnum"} style={it.color ? { ...s.item, color: it.color } : s.item}>
          {it.text}
        </span>
      ))}
    </div>
  );
}
