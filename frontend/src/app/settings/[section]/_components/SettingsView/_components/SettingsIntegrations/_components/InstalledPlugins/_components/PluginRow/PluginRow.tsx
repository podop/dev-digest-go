/* PluginRow — one installed plugin: name (truncated, full name in the native title), version, an
   Enabled/Disabled badge that carries text, the counts, Enable or Disable and Remove (FR17, EC15).
   The row renders the server state only; the hook toasts a failed switch. */
"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KitBadge, MonoAction } from "@/components/settings-kit";
import { useSetPluginEnabled } from "@/lib/hooks";
import type { InstalledPlugin } from "@/lib/types";
import { s } from "./styles";

export function PluginRow({ plugin, onRemove }: { plugin: InstalledPlugin; onRemove: () => void }) {
  const t = useTranslations("integrationsPlugins.row");
  const setEnabled = useSetPluginEnabled();

  const toggle = () => {
    if (setEnabled.isPending) return;
    setEnabled.mutate({ id: plugin.id, enabled: !plugin.enabled });
  };

  return (
    <li style={s.row}>
      <Icon.Boxes size={13} style={s.icon} aria-hidden />
      <div style={s.main}>
        <div style={s.top}>
          <span className="mono" style={s.name} title={plugin.name}>
            {plugin.name}
          </span>
          {plugin.version && (
            <span className="mono" style={s.version}>
              {plugin.version}
            </span>
          )}
        </div>
        <div style={s.meta}>
          <span>{t("counts.agents", { count: plugin.counts.agents })}</span>
          <span>{t("counts.skills", { count: plugin.counts.skills })}</span>
          <span>{t("counts.evalCases", { count: plugin.counts.eval_cases })}</span>
          <span>{t("counts.learnings", { count: plugin.counts.learnings })}</span>
        </div>
      </div>
      <KitBadge color={plugin.enabled ? "var(--ok)" : "var(--text-muted)"} dot>
        {plugin.enabled ? t("status.enabled") : t("status.disabled")}
      </KitBadge>
      <MonoAction
        onClick={toggle}
        disabled={setEnabled.isPending}
        aria-label={plugin.enabled ? t("disableLabel", { name: plugin.name }) : t("enableLabel", { name: plugin.name })}
      >
        {plugin.enabled ? t("disable") : t("enable")}
      </MonoAction>
      <MonoAction
        tone="crit"
        onClick={onRemove}
        disabled={setEnabled.isPending}
        aria-label={t("removeLabel", { name: plugin.name })}
      >
        {t("remove")}
      </MonoAction>
    </li>
  );
}
