/* BlastTree — one collapsible row per changed symbol that has callers (chevron,
   `<> name()`, "N callers"); expanded it lists each caller as a tree line — a
   guide line + "↳" + the GitHub deep-link `file:line` (the caller's symbol name
   is the link's tooltip) — then the endpoint chips (blue, globe) and, apart, the
   cron chips (amber clock). Several symbols can be open at once; the first starts
   open. The row is a real <button aria-expanded>; the links live in the panel,
   never inside it. Layout from the design mockup's BlastRadiusTree. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Icon } from "@devdigest/ui";
import type { BlastCaller, DownstreamImpact } from "@devdigest/shared";
import { callerSymbolLabel } from "../../helpers";
import { TREE_INITIAL_SYMBOLS } from "./constants";
import { s } from "./styles";

export interface BlastTreeProps {
  downstream: DownstreamImpact[];
  /** Deep-link of a caller's line; undefined renders plain text. */
  callerHref: (caller: BlastCaller) => string | undefined;
}

export function BlastTree({ downstream, callerHref }: BlastTreeProps) {
  const t = useTranslations("blast");
  const [open, setOpen] = React.useState<ReadonlySet<string>>(() => new Set(downstream[0] ? [downstream[0].symbol] : []));
  const [showAll, setShowAll] = React.useState(false);
  const baseId = React.useId();
  const listId = `${baseId}-list`;
  const hidden = Math.max(0, downstream.length - TREE_INITIAL_SYMBOLS);
  const visible = showAll ? downstream : downstream.slice(0, TREE_INITIAL_SYMBOLS);

  const toggle = (symbol: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(symbol)) next.add(symbol);
      return next;
    });

  return (
    <div>
      <ul id={listId} style={s.list}>
        {visible.map((d, i) => {
          const isOpen = open.has(d.symbol);
          const panelId = `${baseId}-${i}`;
          const hasChips = d.endpoints_affected.length > 0;
          return (
            <li key={d.symbol}>
              <button
                type="button"
                aria-expanded={isOpen}
                aria-controls={panelId}
                onClick={() => toggle(d.symbol)}
                style={s.row(isOpen)}
              >
                <Icon.ChevronRight size={13} style={s.chevron(isOpen)} />
                <Icon.Code size={13} style={s.codeIcon} />
                <span className="mono" style={s.symbol}>
                  {d.symbol}()
                </span>
                <span style={s.count}>{t("callerCount", { count: d.callers.length })}</span>
              </button>
              {isOpen && (
                <div id={panelId} style={s.panel}>
                  <ul style={s.callers} aria-label={t("callers")}>
                    {d.callers.map((c, ci) => {
                      const href = callerHref(c);
                      const where = `${c.file}:${c.line}`;
                      const symbol = callerSymbolLabel(c);
                      const last = ci === d.callers.length - 1 && !hasChips;
                      return (
                        <li key={`${c.file}:${c.line}:${c.name}`} style={s.caller}>
                          <span aria-hidden="true" style={s.guideV(last)} />
                          <span aria-hidden="true" style={s.guideH} />
                          <Icon.CornerDownRight size={13} style={s.callerIcon} />
                          {href ? (
                            <a
                              className="mono"
                              href={href}
                              target="_blank"
                              rel="noopener noreferrer"
                              title={symbol ?? undefined}
                              style={s.link}
                            >
                              {where}
                            </a>
                          ) : (
                            <span className="mono" title={symbol ?? undefined} style={s.plain}>
                              {where}
                            </span>
                          )}
                        </li>
                      );
                    })}
                  </ul>
                  {hasChips && (
                    <ul style={s.endpoints} aria-label={t("endpointsAffected")}>
                      {d.endpoints_affected.map((e) => (
                        <li key={e}>
                          <Badge icon="Globe" mono color="var(--accent-text)" bg="var(--accent-bg)">
                            {e}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  )}
                  {d.crons_affected.length > 0 && (
                    <ul style={s.crons} aria-label={t("cronsAffected")}>
                      {d.crons_affected.map((c) => (
                        <li key={c}>
                          <Badge icon="Clock" mono color="var(--warn)" bg="var(--warn-bg)">
                            {c}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>
      {hidden > 0 && (
        <button type="button" aria-expanded={showAll} aria-controls={listId} onClick={() => setShowAll((v) => !v)} style={s.more}>
          {showAll ? t("tree.showFewer") : t("tree.showMore", { count: hidden })}
        </button>
      )}
    </div>
  );
}
