import React from "react";
import { Icon } from "../icons";
import { NAV, SETTINGS_ITEM } from "../nav";
import { DefaultLink } from "./DefaultLink";
import type { ShellContext } from "./types";
import { NavItem } from "./NavItem";
import { RepoSwitcher } from "./RepoSwitcher";

export function Sidebar({ ctx }: { ctx: ShellContext }) {
  const Link = ctx.Link ?? DefaultLink;
  return (
    <aside
      style={{
        width: 224,
        flexShrink: 0,
        background: "var(--bg-surface)",
        borderRight: "1px solid var(--border)",
        display: "flex",
        flexDirection: "column",
        padding: "12px 10px 8px",
        gap: 2,
        overflow: "hidden",
      }}
    >
      <Link href="/">
        <div style={{ display: "flex", alignItems: "center", gap: 9, padding: "2px 4px 10px" }}>
          <div
            style={{
              width: 26,
              height: 26,
              borderRadius: 7,
              background: "var(--text-primary)",
              display: "grid",
              placeItems: "center",
              flexShrink: 0,
            }}
          >
            <Icon.Layers size={15} style={{ color: "var(--bg-primary)" }} />
          </div>
          <span style={{ fontSize: 15, fontWeight: 700, letterSpacing: "-0.02em" }}>DevDigest</span>
        </div>
      </Link>
      <RepoSwitcher ctx={ctx} />
      <div style={{ overflowY: "auto", flex: 1, margin: "4px -4px 0", padding: "0 4px" }}>
        {NAV.map((grp, gi) => (
          <div key={gi} style={{ marginBottom: 10 }}>
            <div
              style={{
                fontSize: 10,
                fontWeight: 700,
                letterSpacing: "0.08em",
                color: "var(--text-muted)",
                padding: "0 9px",
                marginBottom: 4,
              }}
            >
              {grp.section}
            </div>
            {grp.items.map((it) => (
              <NavItem
                key={it.key}
                item={it.key === "pulls" && ctx.prCount != null ? { ...it, badge: String(ctx.prCount) } : it}
                active={ctx.activeKey === it.key}
                repoId={ctx.repoId}
                Link={Link}
              />
            ))}
          </div>
        ))}
      </div>
      <div style={{ borderTop: "1px solid var(--border)", paddingTop: 6, marginTop: 2 }}>
        <NavItem item={SETTINGS_ITEM} active={ctx.activeKey === "settings"} repoId={ctx.repoId} Link={Link} />
      </div>
    </aside>
  );
}
