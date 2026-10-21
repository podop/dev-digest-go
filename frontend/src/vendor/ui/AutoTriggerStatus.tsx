/* AutoTriggerStatus — pill showing whether automatic reviews are ON/OFF, with a
   pulsing dot and an optional polling-interval detail. Ported from components2.jsx. */
import React from "react";

export function AutoTriggerStatus({
  on = true,
  detail,
}: {
  on?: boolean;
  detail?: string;
}) {
  return (
    <button
      title="Settings → Automatic Reviews"
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 10,
        padding: "6px 12px",
        borderRadius: 7,
        border: "1px solid var(--border)",
        background: "var(--bg-surface)",
        fontSize: 13,
        color: "var(--text-secondary)",
      }}
    >
      <span
        style={{
          width: 7,
          height: 7,
          borderRadius: 99,
          background: on ? "var(--ok)" : "var(--text-muted)",
          boxShadow: on ? "0 0 0 3px var(--ok-bg)" : "none",
          animation: on ? "ddpulse 2s ease-in-out infinite" : "none",
        }}
      />
      <span>
        Auto-review:{" "}
        <b style={{ color: on ? "var(--ok)" : "var(--text-muted)", fontWeight: 600 }}>{on ? "ON" : "OFF"}</b>
      </span>
      {on && <span style={{ color: "var(--text-muted)" }}>· {detail ?? "polling 5m"}</span>}
    </button>
  );
}
