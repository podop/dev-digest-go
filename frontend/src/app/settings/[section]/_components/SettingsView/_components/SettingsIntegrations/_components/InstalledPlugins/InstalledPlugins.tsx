/* InstalledPlugins — rendered inside the Plugin Import card (prototype): the plugins of GET /plugins with their loading / empty / error states, a
   row each (FR17) and the Remove entry point. State shown comes from the server only: a failed
   switch leaves the row as it was (nothing is written optimistically). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { ErrorState, Skeleton } from "@devdigest/ui";
import { usePlugins } from "@/lib/hooks";
import type { InstalledPlugin } from "@/lib/types";
import { RemovePluginDialog } from "../RemovePluginDialog";
import { PluginRow } from "./_components/PluginRow";
import { s } from "./styles";

export function InstalledPlugins() {
  const t = useTranslations("integrationsPlugins");
  const tc = useTranslations("common");
  const plugins = usePlugins();
  const [removing, setRemoving] = React.useState<InstalledPlugin | null>(null);
  const list = plugins.data?.plugins ?? [];

  return (
    <section aria-labelledby="int-plugins-title">
      <h4 id="int-plugins-title" style={s.title}>
        {t("title")}
      </h4>

      {plugins.isPending ? (
        <div role="status" aria-label={t("loading")} style={s.skeletons}>
          <Skeleton height={34} />
        </div>
      ) : plugins.isError ? (
        <ErrorState title={t("loadError")} onRetry={() => void plugins.refetch()} retryLabel={tc("actions.retry")} />
      ) : list.length === 0 ? (
        <div style={s.empty}>
          <span style={s.emptyTitle}>{t("empty")}</span>
          <span>{t("emptyHint")}</span>
        </div>
      ) : (
        <ul style={s.list}>
          {list.map((plugin) => (
            <PluginRow key={plugin.id} plugin={plugin} onRemove={() => setRemoving(plugin)} />
          ))}
        </ul>
      )}

      {removing && <RemovePluginDialog plugin={removing} onClose={() => setRemoving(null)} />}
    </section>
  );
}
