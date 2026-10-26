/* PluginBadge — "From plugin <name>" label of an agent or skill that a plugin owns (Settings ›
   Integrations, FR18). A switched-off plugin adds a "plugin disabled" badge, as text so it never
   rests on colour alone. Unowned items render nothing. Shared by the agent and skill cards and
   editors. */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import type { PluginRef } from "@devdigest/shared";
import { s } from "./styles";

export function PluginBadge({ plugin }: { plugin?: PluginRef | null }) {
  const t = useTranslations("pluginBadge");
  if (!plugin) return null;
  const label = t("label", { name: plugin.name });
  return (
    <span style={s.wrap}>
      <span title={label} style={s.titled}>
        <Badge color="var(--text-secondary)" icon="Boxes" style={s.badge}>
          <span style={s.text}>{label}</span>
        </Badge>
      </span>
      {!plugin.enabled && (
        <Badge color="var(--warn)" icon="AlertTriangle">
          {t("disabled")}
        </Badge>
      )}
    </span>
  );
}
