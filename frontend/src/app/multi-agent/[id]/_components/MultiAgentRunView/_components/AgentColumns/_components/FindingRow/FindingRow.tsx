/* FindingRow — one finding inside a column: severity, title, file:line. Text only (titles are model output). */
import React from "react";
import { SeverityBadge, type Severity } from "@devdigest/ui";
import type { FindingRecord } from "@devdigest/shared";
import { s } from "./styles";

export function FindingRow({ finding }: { finding: FindingRecord }) {
  return (
    <li style={s.row} data-finding-id={finding.id}>
      <SeverityBadge severity={finding.severity as Severity} />
      <span style={s.title}>{finding.title}</span>
      <span className="mono" style={s.location}>
        {finding.file}:{finding.start_line}
      </span>
    </li>
  );
}
