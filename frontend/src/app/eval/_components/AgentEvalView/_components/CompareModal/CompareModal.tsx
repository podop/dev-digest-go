/* CompareModal — runs A (older) → B (newer) side by side (FR25): each metric and the cost
   old → new with a signed delta, per-case status with flips, and the system prompt line
   diff plus model / strategy / skills changes. All run, case, prompt and skill text is
   data from untrusted PR content and renders as plain text (NFR7). The vendored Modal has
   no Escape handling, so Escape closes it here and a labelled Close button is reachable
   by keyboard (NFR8). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Modal, Skeleton } from "@devdigest/ui";
import type { EvalCompare, EvalPromptDiffLine } from "@devdigest/shared";
import { directionOf, EVAL_METRICS, toPercent, type DeltaDirection } from "@/components/eval-metrics";
import { useEvalCompare } from "@/lib/hooks";
import { formatUsd } from "@/lib/format-usage";
import { METRIC_LABEL_KEY } from "../../constants";
import { flippedCount, isConfigUnchanged, isPromptUnchanged, metricChanges } from "./helpers";
import { s } from "./styles";

const TONE: Record<DeltaDirection, React.CSSProperties> = { up: s.up, down: s.down, flat: s.flat };
const DELTA_SUFFIX: Record<DeltaDirection, string> = { up: "Up", down: "Down", flat: "Flat" };
const DIFF_STYLE: Record<EvalPromptDiffLine["op"], React.CSSProperties> = { same: {}, add: s.add, del: s.del };
const DIFF_MARKER_KEY: Record<EvalPromptDiffLine["op"], string> = { same: "diffSame", add: "diffAdd", del: "diffDel" };

export function CompareModal({ aId, bId, onClose }: { aId: string; bId: string; onClose: () => void }) {
  const t = useTranslations("eval.compare");
  const tc = useTranslations("common");
  const compare = useEvalCompare(aId, bId);
  const bodyRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Move focus into the dialog so the next Tab lands on its controls, not behind it.
  React.useEffect(() => bodyRef.current?.focus(), []);

  const data = compare.data;
  return (
    <Modal
      width={820}
      title={data ? t("title", { a: data.a.agent_version, b: data.b.agent_version }) : t("heading")}
      onClose={onClose}
      footer={
        <div style={s.footer}>
          <Button kind="ghost" onClick={onClose}>
            {t("close")}
          </Button>
        </div>
      }
    >
      <div ref={bodyRef} tabIndex={-1} style={s.body}>
        {compare.isLoading && <Skeleton height={180} />}
        {compare.isError && <ErrorState title={t("loadError")} onRetry={() => compare.refetch()} retryLabel={tc("actions.retry")} />}
        {data && <CompareBody compare={data} />}
      </div>
    </Modal>
  );
}

function CompareBody({ compare }: { compare: EvalCompare }) {
  const t = useTranslations("eval.compare");
  const flipped = flippedCount(compare);
  return (
    <>
      <MetricsSection compare={compare} />

      <section style={s.section} aria-label={t("casesHeading")}>
        <h3 style={s.h3}>{t("casesHeading")}</h3>
        <p style={s.muted}>{t("flippedSummary", { count: flipped })}</p>
        <table style={s.table}>
          <tbody>
            {compare.cases.map((c) => (
              <tr key={c.case_id} data-testid="compare-case">
                <th scope="row" style={{ ...s.td, textAlign: "left", fontWeight: c.flipped ? 700 : 400 }}>{c.case_name}</th>
                <td style={s.td}>{t("change", { from: t(`status.${c.a}`), to: t(`status.${c.b}`) })}</td>
                <td style={{ ...s.td, ...s.flipped }}>{c.flipped && t("flipped")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section style={s.section} aria-label={t("promptHeading")}>
        <h3 style={s.h3}>{t("promptHeading")}</h3>
        {isPromptUnchanged(compare) ? (
          <p style={s.muted}>{t("promptUnchanged")}</p>
        ) : (
          <div style={s.diff} className="mono" data-testid="prompt-diff">
            {compare.config_diff.system_prompt.map((l, i) => (
              <div key={i} style={{ ...s.line, ...DIFF_STYLE[l.op] }}>
                <span style={s.marker}>{t(DIFF_MARKER_KEY[l.op])}</span>
                <span>{l.line}</span>
              </div>
            ))}
          </div>
        )}
      </section>

      <ConfigSection compare={compare} />
    </>
  );
}

function MetricsSection({ compare }: { compare: EvalCompare }) {
  const t = useTranslations("eval.compare");
  const tm = useTranslations("eval.metricTiles");
  const changes = metricChanges(compare, EVAL_METRICS);
  const percent = (ratio: number | null) => {
    const value = toPercent(ratio);
    return value == null ? tm("noValue") : tm("percent", { value });
  };
  const cost = compare.delta.cost_usd;
  const costDirection = cost == null ? null : directionOf(cost);
  return (
    <section style={s.section} aria-label={t("metricsHeading")}>
      <h3 style={s.h3}>{t("metricsHeading")}</h3>
      <table style={s.table}>
        <tbody>
          {changes.map(({ metric, from, to, points }) => {
            const direction = points == null ? null : directionOf(points);
            return (
              <tr key={metric}>
                <th scope="row" style={{ ...s.td, textAlign: "left", fontWeight: 600 }}>{tm(METRIC_LABEL_KEY[metric])}</th>
                <td className="tnum" style={s.td}>{t("change", { from: percent(from), to: percent(to) })}</td>
                <td className="tnum" style={{ ...s.td, ...(direction ? TONE[direction] : s.flat) }}>
                  {points == null || direction == null ? tm("noValue") : tm(`delta${DELTA_SUFFIX[direction]}`, { amount: Math.abs(points) })}
                </td>
              </tr>
            );
          })}
          <tr>
            <th scope="row" style={{ ...s.td, textAlign: "left", fontWeight: 600 }}>{t("cost")}</th>
            <td className="tnum" style={s.td}>{t("change", { from: formatUsd(compare.a.cost_usd), to: formatUsd(compare.b.cost_usd) })}</td>
            <td className="tnum" style={{ ...s.td, ...s.flat }}>
              {cost == null || costDirection == null ? tm("noValue") : t(`cost${DELTA_SUFFIX[costDirection]}`, { amount: formatUsd(Math.abs(cost)) })}
            </td>
          </tr>
        </tbody>
      </table>
    </section>
  );
}

function ConfigSection({ compare }: { compare: EvalCompare }) {
  const t = useTranslations("eval.compare");
  const { model, strategy, skills_added, skills_removed } = compare.config_diff;
  return (
    <section style={s.section} aria-label={t("configHeading")}>
      <h3 style={s.h3}>{t("configHeading")}</h3>
      {isConfigUnchanged(compare) ? (
        <p style={s.muted}>{t("configUnchanged")}</p>
      ) : (
        <ul style={s.list}>
          {model && <li>{t("labelled", { label: t("model"), value: t("change", { from: model.a, to: model.b }) })}</li>}
          {strategy && <li>{t("labelled", { label: t("strategy"), value: t("change", { from: strategy.a, to: strategy.b }) })}</li>}
          {skills_added.length > 0 && <li>{t("labelled", { label: t("skillsAdded"), value: skills_added.join(", ") })}</li>}
          {skills_removed.length > 0 && <li>{t("labelled", { label: t("skillsRemoved"), value: skills_removed.join(", ") })}</li>}
        </ul>
      )}
    </section>
  );
}
