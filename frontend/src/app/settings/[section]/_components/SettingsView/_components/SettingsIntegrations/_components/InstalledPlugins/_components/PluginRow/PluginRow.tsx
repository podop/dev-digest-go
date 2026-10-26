/* PluginRow — one installed plugin: name (truncated, full name in the native title), version, an
   Enabled/Disabled badge that carries text, the counts, Enable or Disable and Remove (FR17, EC15).
   The row renders the server state only; the hook toasts a failed switch. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, Button } from "@devdigest/ui";
import { useSetPluginEnabled } from "@/lib/hooks";
import type { InstalledPlugin } from "@/lib/types";
import { s } from "./styles";

export function PluginRow({ plugin, onRemove }: { plugin: InstalledPlugin; onRemove: () => void }) {
  const t = useTranslations("integrationsPlugins.row");
  const setEnabled = useSetPluginEnabled();
  const badge = plugin.enabled ? s.enabledBadge : s.disabledBadge;

  const toggle = () => {
    if (setEnabled.isPending) return;
    setEnabled.mutate({ id: plugin.id, enabled: !plugin.enabled });
  };

  return (
    <li style={s.row}>
      <div style={s.main}>
        <div style={s.top}>
          <span style={s.name} title={plugin.name}>
            {plugin.name}
          </span>
          {plugin.version && <span style={s.version}>{plugin.version}</span>}
          <Badge color={badge.color} bg={badge.bg}>
            {plugin.enabled ? t("status.enabled") : t("status.disabled")}
          </Badge>
        </div>
        <div style={s.meta}>
          <span>{t("counts.agents", { count: plugin.counts.agents })}</span>
          <span>{t("counts.skills", { count: plugin.counts.skills })}</span>
          <span>{t("counts.evalCases", { count: plugin.counts.eval_cases })}</span>
          <span>{t("counts.learnings", { count: plugin.counts.learnings })}</span>
        </div>
      </div>
      <div style={s.actions}>
        <Button
          type="button"
          kind="secondary"
          size="sm"
          onClick={toggle}
          disabled={setEnabled.isPending}
          aria-label={plugin.enabled ? t("disableLabel", { name: plugin.name }) : t("enableLabel", { name: plugin.name })}
        >
          {plugin.enabled ? t("disable") : t("enable")}
        </Button>
        <Button
          type="button"
          kind="ghost"
          size="sm"
          onClick={onRemove}
          disabled={setEnabled.isPending}
          aria-label={t("removeLabel", { name: plugin.name })}
        >
          {t("remove")}
        </Button>
      </div>
    </li>
  );
}
