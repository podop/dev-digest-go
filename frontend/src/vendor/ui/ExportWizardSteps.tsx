/* ExportWizardSteps — horizontal numbered step indicator for export/publish
   wizards. Ported from components2.jsx. */
import React from "react";
import { Icon } from "./icons";

export function ExportWizardSteps({ step, labels }: { step: number; labels: string[] }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 0, padding: "0 5px" }}>
      {labels.map((l, i) => (
        <React.Fragment key={i}>
          <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <div
              style={{
                width: 24,
                height: 24,
                borderRadius: 99,
                display: "grid",
                placeItems: "center",
                fontSize: 12,
                fontWeight: 700,
                flexShrink: 0,
                background: i < step ? "var(--ok)" : i === step ? "var(--accent)" : "var(--bg-elevated)",
                color: i <= step ? "#fff" : "var(--text-muted)",
                border: i > step ? "1px solid var(--border-strong)" : "none",
              }}
            >
              {i < step ? <Icon.Check size={13} /> : i + 1}
            </div>
            <span
              style={{
                fontSize: 13,
                fontWeight: i === step ? 600 : 500,
                color: i <= step ? "var(--text-primary)" : "var(--text-muted)",
                whiteSpace: "nowrap",
              }}
            >
              {l}
            </span>
          </div>
          {i < labels.length - 1 && (
            <div
              style={{
                flex: 1,
                height: 1,
                minWidth: 24,
                background: i < step ? "var(--ok)" : "var(--border-strong)",
                margin: "0 14px",
              }}
            />
          )}
        </React.Fragment>
      ))}
    </div>
  );
}
