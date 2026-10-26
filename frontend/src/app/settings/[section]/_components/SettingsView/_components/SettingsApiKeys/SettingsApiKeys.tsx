"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KeyField, KEY_PAGE_URLS } from "@/components/key-field";
import { useAbout, useSecretsStatus } from "@/lib/hooks";
import { sectionHref } from "../../constants";
import { SectionTitle } from "../SectionTitle";
import { CustomProviders } from "./_components/CustomProviders";
import { DefaultModelField } from "./_components/DefaultModelField";
import { SpendCapField } from "./_components/SpendCapField";
import { KEY_ROWS } from "./constants";
import { s } from "./styles";

/** FR15: where the keys live (real path from GET /about) and where to put them for CI. */
function SecretsInfoBox({ secretsFile }: { secretsFile: string }) {
  const t = useTranslations("apiKeys");
  return (
    <div style={s.infoBox}>
      <Icon.Info size={14} style={s.infoIcon} />
      <p>
        {t.rich("infoBox", {
          path: () => <span style={s.path}>{secretsFile}</span>,
          github: (chunks) => (
            <Link href={sectionHref("github")} style={s.link}>
              {chunks}
            </Link>
          ),
        })}
      </p>
    </div>
  );
}

/** Settings → API Keys: provider keys (FR2–FR4), custom providers (custom-providers FR16), default model (FR8), monthly spend cap (FR11), secrets info box (FR15). */
export function SettingsApiKeys() {
  const t = useTranslations("apiKeys");
  const { data: status } = useSecretsStatus();
  const { data: about } = useAbout();
  return (
    <div style={s.wrap}>
      <SectionTitle title={t("title")} body={t("body")} />
      {KEY_ROWS.map((row) => (
        <KeyField
          key={row.provider}
          provider={row.provider}
          label={t(row.labelKey)}
          hint={t(row.hintKey)}
          docsHref={KEY_PAGE_URLS[row.provider]}
          configured={status?.[row.provider]}
          optional={row.optional}
        />
      ))}
      <CustomProviders />
      <DefaultModelField />
      <SpendCapField />
      {about && <SecretsInfoBox secretsFile={about.secrets_file} />}
    </div>
  );
}
