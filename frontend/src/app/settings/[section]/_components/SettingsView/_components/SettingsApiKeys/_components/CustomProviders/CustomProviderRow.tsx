/* One custom provider row (FR16): name, id, protocol, host, key badge, plain-http warning, non-strict
   structured-output mode, and Test / Edit / Delete. Test runs the connection test of the stored provider
   and shows the FR23 banner under the row. */
"use client";

import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
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
        <span style={s.name}>{name}</span>
        <span style={s.mono}>{provider.id}</span>
        <span style={s.spacer} />
        <span style={s.badge(tone)}>
          <span aria-hidden style={s.badgeDot(tone)} />
          {provider.key_configured ? t("row.keySaved") : t("row.noKey")}
        </span>
      </div>
      <div style={s.meta}>
        <span>{t(`protocol.${provider.protocol}`)}</span>
        <span style={s.mono}>{hostOf(provider.base_url)}</span>
        {provider.structured_output !== "strict" && (
          <span>{t("row.structuredOutput", { mode: t(`modes.${provider.structured_output}`) })}</span>
        )}
      </div>
      {provider.plain_http && (
        <div role="note" style={s.warn}>
          <Icon.AlertTriangle size={13} />
          {t("row.plainHttp")}
        </div>
      )}
      <div style={s.actions}>
        <Button type="button" kind="secondary" size="sm" disabled={pending} aria-label={t("row.testAria", { name })} onClick={() => void run("")}>
          {pending ? t("row.testing") : t("row.test")}
        </Button>
        <Button type="button" kind="secondary" size="sm" aria-label={t("row.editAria", { name })} onClick={onEdit}>
          {t("row.edit")}
        </Button>
        <Button type="button" kind="danger" size="sm" aria-label={t("row.deleteAria", { name })} onClick={onDelete}>
          {t("row.delete")}
        </Button>
      </div>
      {result && !pending && <TestResultBanner result={result} name={name} />}
    </li>
  );
}
