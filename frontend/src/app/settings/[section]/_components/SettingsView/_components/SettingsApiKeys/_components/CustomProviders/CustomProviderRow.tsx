/* One custom provider row (FR16): name, id, protocol, host, key badge, plain-http warning, non-strict
   structured-output mode, and Test / Edit / Delete. Test runs the connection test of the stored provider
   and shows the FR23 banner under the row. */
"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KitBadge, MonoAction } from "@/components/settings-kit";
import { useKeyTest } from "@/components/key-field/useKeyTest";
import { hostOf } from "@/lib/providers/custom";
import type { CustomProvider } from "@/lib/types";
import { TestResultBanner } from "../CustomProviderDialog";
import { s } from "./styles";

export function CustomProviderRow({
  provider,
  onEdit,
  onDelete,
}: {
  provider: CustomProvider;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const t = useTranslations("customProviders");
  const { run, result, pending } = useKeyTest(provider.id);
  const tone = provider.key_configured ? "ok" : "muted";
  const { name } = provider;

  return (
    <li style={s.row}>
      <div style={s.rowHead}>
        <Icon.Cpu size={13} style={s.rowIcon} aria-hidden />
        <span style={s.name}>{name}</span>
        <span className="mono" style={s.id}>
          {provider.id}
        </span>
        <span style={s.spacer} />
        <KitBadge dot color={tone === "ok" ? "var(--ok)" : "var(--text-muted)"} bg="transparent">
          {provider.key_configured ? t("row.keySaved") : t("row.noKey")}
        </KitBadge>
        <MonoAction disabled={pending} aria-label={t("row.testAria", { name })} onClick={() => void run("")}>
          {pending ? t("row.testing") : t("row.test")}
        </MonoAction>
        <MonoAction aria-label={t("row.editAria", { name })} onClick={onEdit}>
          {t("row.edit")}
        </MonoAction>
        <MonoAction tone="crit" aria-label={t("row.deleteAria", { name })} onClick={onDelete}>
          {t("row.delete")}
        </MonoAction>
      </div>
      <div style={s.meta}>
        <span>{t(`protocol.${provider.protocol}`)}</span>
        <span className="mono">{hostOf(provider.base_url)}</span>
        {provider.structured_output !== "strict" && (
          <span>{t("row.structuredOutput", { mode: t(`modes.${provider.structured_output}`) })}</span>
        )}
      </div>
      {provider.plain_http && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={13} aria-hidden />
          {t("row.plainHttp")}
        </div>
      )}
      {result && !pending && <TestResultBanner result={result} name={name} />}
    </li>
  );
}
