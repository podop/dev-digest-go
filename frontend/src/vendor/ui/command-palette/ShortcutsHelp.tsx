/* ShortcutsHelp — keyboard-shortcuts cheat sheet overlay (triggered by `?`). */
import React from "react";
import { Kbd } from "../primitives";
import { SHORTCUTS } from "../nav";

export function ShortcutsHelp({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null;
  const groups = Array.from(new Set(SHORTCUTS.map((s) => s.group)));
  return (
    <div style={{ position: "fixed", inset: 0, display: "grid", placeItems: "center", zIndex: 60, padding: 28 }}>
      <div onClick={onClose} style={{ position: "absolute", inset: 0, background: "rgba(0,0,0,0.5)" }} />
      <div
        role="dialog"
        aria-label="Keyboard shortcuts"
        style={{
          position: "relative",
          width: 520,
          maxWidth: "100%",
          background: "var(--bg-elevated)",
          border: "1px solid var(--border-strong)",
          borderRadius: 12,
          boxShadow: "var(--shadow-modal)",
          padding: 24,
          animation: "ddpop .14s ease",
        }}
      >
        <div style={{ fontSize: 16, fontWeight: 700, marginBottom: 18 }}>Keyboard shortcuts</div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "18px 24px" }}>
          {groups.map((g) => (
            <div key={g}>
              <div
                style={{
                  fontSize: 12,
                  fontWeight: 700,
                  letterSpacing: "0.06em",
                  textTransform: "uppercase",
                  color: "var(--text-muted)",
                  marginBottom: 10,
                }}
              >
                {g}
              </div>
              <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                {SHORTCUTS.filter((s) => s.group === g).map((s) => (
                  <div key={s.label} style={{ display: "flex", alignItems: "center", gap: 10, fontSize: 13 }}>
                    <span style={{ flex: 1, color: "var(--text-secondary)" }}>{s.label}</span>
                    {s.keys.split(" ").map((k, i) => (
                      <Kbd key={i}>{k}</Kbd>
                    ))}
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
