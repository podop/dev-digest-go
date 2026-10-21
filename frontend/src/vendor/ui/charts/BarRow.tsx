/* BarRow — single labelled horizontal bar (lightweight inline SVG/divs). */
import React from "react";

export function BarRow({
  label,
  value,
  max,
  color = "var(--accent)",
  suffix,
}: {
  label: string;
  value: number;
  max: number;
  color?: string;
  suffix?: string;
}) {
  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "150px 1fr 70px",
        alignItems: "center",
        gap: 14,
        padding: "6px 0",
      }}
    >
      <span
        className="mono"
        style={{
          fontSize: 13,
          color: "var(--text-secondary)",
          whiteSpace: "nowrap",
          overflow: "hidden",
          textOverflow: "ellipsis",
        }}
      >
        {label}
      </span>
      <div style={{ height: 10, background: "var(--bg-hover)", borderRadius: 3, overflow: "hidden" }}>
        <div style={{ width: (value / max) * 100 + "%", height: "100%", background: color, borderRadius: 3 }} />
      </div>
      <span className="mono tnum" style={{ fontSize: 13, textAlign: "right", fontWeight: 600 }}>
        {suffix || ""}
      </span>
    </div>
  );
}
