/* RiskAreas — the brief's risks, shown inside the Intent card (slot): a "RISK AREAS" label,
   then one compact chip per risk (severity icon + title over its file refs) with a chevron
   button that expands the explanation. A file ref opens Files changed at that line. Renders
   nothing without a brief (the PR Brief card owns the generating skeleton); while an existing
   brief is regenerated its risks stay. All text is plain text. */
"use client";

import { useId, useState } from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { Risk } from "@devdigest/shared";
import { usePrBrief } from "@/lib/hooks";
import { parseFileRef } from "../../helpers";
import type { OpenInDiff } from "../../useOpenInDiff";
import { SEVERITY_META } from "./constants";
import { s } from "./styles";

export interface RiskAreasProps extends OpenInDiff {
  prId: string;
}

/** Unique per risk within one brief (the model returns no ids): the position keeps identical risks apart. */
const riskId = (r: Risk, i: number) => `risk:${i}:${r.severity}:${r.title}:${r.file_refs.join(",")}`;

export function RiskAreas({ prId, open, missingKey }: RiskAreasProps) {
  const t = useTranslations("brief");
  const { data } = usePrBrief(prId);
  const brief = data?.brief ?? null;

  if (!brief) return null;

  return (
    <section style={s.section} aria-label={t("block.risks")}>
      <div style={s.header}>
        <Icon.AlertTriangle size={14} style={s.headerIcon} aria-hidden="true" />
        <h3 style={s.headerTitle}>{t("block.risks")}</h3>
      </div>
      {brief.risks.risks.length === 0 ? (
        <p style={s.empty}>{t("noRisks")}</p>
      ) : (
        <ul style={s.list}>
          {brief.risks.risks.map((risk, i) => {
            const id = riskId(risk, i);
            return <RiskChip key={id} id={id} risk={risk} open={open} missingKey={missingKey} />;
          })}
        </ul>
      )}
    </section>
  );
}

function RiskChip({ id, risk, open, missingKey }: { id: string; risk: Risk } & OpenInDiff) {
  const t = useTranslations("brief");
  const [expanded, setExpanded] = useState(false);
  const panelId = useId();
  const meta = SEVERITY_META[risk.severity];
  const SevIcon = Icon[meta.icon];
  const severity = t(`risk.severity.${risk.severity}`);
  return (
    <li style={s.item}>
      <div style={s.chip}>
        <div style={s.main}>
          <div style={s.titleRow}>
            <span role="img" aria-label={severity} title={severity} style={s.icon(meta.color)}>
              <SevIcon size={14} aria-hidden="true" />
            </span>
            <span style={s.title}>{risk.title}</span>
          </div>
          {risk.file_refs.map((ref) => {
            const target = parseFileRef(ref);
            return target ? (
              <button key={ref} type="button" className="mono" style={s.ref} title={ref} onClick={() => open(`${id}|${ref}`, target)}>
                {ref}
              </button>
            ) : (
              <span key={ref} className="mono" style={s.refPlain} title={ref}>
                {ref}
              </span>
            );
          })}
        </div>
        <button
          type="button"
          style={s.toggle}
          aria-label={risk.title}
          aria-expanded={expanded}
          aria-controls={expanded ? panelId : undefined}
          onClick={() => setExpanded((v) => !v)}
        >
          <Icon.ChevronDown size={14} style={s.chevron(expanded)} aria-hidden="true" />
        </button>
      </div>
      {risk.file_refs.some((ref) => missingKey === `${id}|${ref}`) && <span style={s.notInDiff}>{t("notInDiff")}</span>}
      {expanded && (
        <p id={panelId} style={s.explanation}>
          {risk.explanation}
        </p>
      )}
    </li>
  );
}
