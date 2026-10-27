/* FindingRow — one finding inside a column: severity icon (named for screen readers), title,
   file:line (prototype AgentFindingMini). Text only (titles are model output). */
import { Icon, SEV, type Severity } from "@devdigest/ui";
import type { FindingRecord } from "@devdigest/shared";
import { s } from "./styles";

export function FindingRow({ finding }: { finding: FindingRecord }) {
  const sev = SEV[finding.severity as Severity] ?? SEV.INFO;
  const I = Icon[sev.icon];
  return (
    <li style={s.row(sev.c)} data-finding-id={finding.id}>
      <div style={s.titleRow}>
        <span role="img" aria-label={sev.label} title={sev.label} style={s.icon(sev.c)}>
          <I size={12} aria-hidden />
        </span>
        <span style={s.title}>{finding.title}</span>
      </div>
      <div className="mono" style={s.location}>
        {finding.file}:{finding.start_line}
      </div>
    </li>
  );
}
