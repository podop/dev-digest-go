/* LiveLogStream — scrollable, filterable, copyable run-log viewer with a
   "running" pulse. Ported from components2.jsx. */
import React from "react";
import { Icon } from "./icons";
import { IconBtn } from "./primitives";

export interface LogLine {
  t: string;
  k: "info" | "result" | "tool" | "error";
  m: string;
}

const LOG_COLOR: Record<LogLine["k"], string> = {
  info: "var(--accent-text)",
  result: "var(--ok)",
  tool: "var(--warn)",
  error: "var(--crit)",
};

export function LiveLogStream({
  log,
  running,
  height = 260,
  elapsedLabel,
}: {
  log: LogLine[];
  running?: boolean;
  height?: number;
  elapsedLabel?: string;
}) {
  const [filter, setFilter] = React.useState("");
  const shown = filter.trim()
    ? log.filter((l) => l.m.toLowerCase().includes(filter.toLowerCase()) || l.k.includes(filter.toLowerCase()))
    : log;

  // Copy the (filtered) log to the clipboard, with brief visual confirmation.
  const [copied, setCopied] = React.useState(false);
  const copyLog = () => {
    const text = shown.map((l) => `[${l.t}] [${l.k}] ${l.m}`).join("\n");
    void navigator.clipboard?.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <div style={{ border: "1px solid var(--border)", borderRadius: 8, overflow: "hidden", background: "var(--code-bg)" }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          padding: "10px 14px",
          borderBottom: "1px solid var(--border)",
          background: "var(--bg-surface)",
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            padding: "4px 10px",
            borderRadius: 6,
            border: "1px solid var(--border)",
            background: "var(--bg-elevated)",
            width: 170,
          }}
        >
          <Icon.Search size={12} style={{ color: "var(--text-muted)" }} />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter log…"
            style={{
              flex: 1,
              fontSize: 12,
              color: "var(--text-primary)",
              background: "transparent",
              border: "none",
              outline: "none",
            }}
          />
        </div>
        {running ? (
          <span
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: 8,
              fontSize: 12,
              color: "var(--warn)",
              fontWeight: 600,
            }}
          >
            <span style={{ width: 7, height: 7, borderRadius: 99, background: "var(--warn)", animation: "ddpulse 1s infinite" }} />
            {elapsedLabel ?? "Running"}
          </span>
        ) : (
          <span style={{ fontSize: 12, color: "var(--text-muted)" }}>{shown.length} lines</span>
        )}
        <div style={{ marginLeft: "auto", display: "flex", gap: 2 }}>
          <IconBtn
            icon={copied ? "Check" : "Copy"}
            label={copied ? "Copied!" : "Copy log"}
            size={26}
            onClick={copyLog}
          />
        </div>
      </div>
      <div style={{ height, overflow: "auto", padding: "12px 14px", display: "flex", flexDirection: "column", gap: 4 }}>
        {shown.map((l, i) => (
          <div
            key={i}
            className="mono"
            style={{ fontSize: 12, lineHeight: 1.5, display: "flex", gap: 10, animation: "ddfadein .2s ease" }}
          >
            <span style={{ color: "var(--text-muted)", flexShrink: 0 }}>[{l.t}]</span>
            <span style={{ color: LOG_COLOR[l.k], flexShrink: 0, fontWeight: 600 }}>[{l.k}]</span>
            <span style={{ color: "var(--text-primary)" }}>{l.m}</span>
          </div>
        ))}
        {running && (
          <div className="mono" style={{ fontSize: 12, color: "var(--text-muted)" }}>
            <span
              style={{
                display: "inline-block",
                width: 7,
                height: 13,
                background: "var(--ok)",
                animation: "ddpulse 1s infinite",
                verticalAlign: "middle",
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}
