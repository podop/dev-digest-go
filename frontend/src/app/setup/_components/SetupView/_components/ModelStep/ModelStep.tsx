"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KEY_PAGE_URLS, KeyField } from "@/components/key-field";
import type { ConnTestProvider, ConnTestResult, SecretsStatus } from "@/lib/types";
import { MODEL_PROVIDERS, type ModelProvider } from "../../constants";
import { initialProvider } from "../../helpers";
import { Callout } from "@/components/settings-kit";
import { ChoiceGroup } from "../ChoiceGroup";
import { StepFrame } from "../StepFrame";
import { s } from "./styles";

const CUSTOM_PROVIDERS_HREF = "/settings/api-keys";

/** Step 1 (FR23): choose a provider, test its key. Continue needs a passing test of the chosen
 *  provider, or a stored key that no test of this view has failed. */
export function ModelStep({
  secrets,
  customCount = 0,
  onTested,
  onContinue,
}: {
  secrets: SecretsStatus;
  /** Defined custom providers: one is enough to continue, keyed or not (FR21, D21). */
  customCount?: number;
  onTested: (provider: ConnTestProvider) => void;
  onContinue?: () => void;
}) {
  const t = useTranslations("setup");
  const [provider, setProvider] = React.useState<ModelProvider>(() => initialProvider(secrets));
  // Last test outcome of this view, per provider; switching provider starts from the stored state (EC12).
  const [outcomes, setOutcomes] = React.useState<Partial<Record<ConnTestProvider, boolean>>>({});

  const outcome = outcomes[provider];
  const ready = customCount > 0 || (outcome ?? secrets[provider]);
  const alreadyConnected = secrets[provider] && outcome === undefined;

  const onResult = (result: ConnTestResult) => {
    setOutcomes((prev) => ({ ...prev, [result.provider]: result.ok }));
    if (result.ok) onTested(result.provider);
  };

  return (
    <StepFrame
      title={t("model.title")}
      sub={t("model.sub")}
      primary={{ label: t("actions.continue"), onClick: () => onContinue?.(), disabled: !ready }}
    >
      <ChoiceGroup
        legend={t("model.providerLegend")}
        name="setup-provider"
        options={MODEL_PROVIDERS}
        value={provider}
        onChange={setProvider}
        labelOf={(p) => t(`model.providers.${p}`)}
        right={
          <Link href={CUSTOM_PROVIDERS_HREF} style={s.customLink}>
            {t("model.customLink")}
            <Icon.ArrowRight size={11} aria-hidden />
          </Link>
        }
      />
      <KeyField
        key={provider}
        provider={provider}
        label={t("model.keyLabel", { provider: t(`model.providers.${provider}`) })}
        hint={t("storedLocally")}
        docsHref={KEY_PAGE_URLS[provider]}
        configured={secrets[provider]}
        onResult={onResult}
      />
      {alreadyConnected && <Callout tone="ok">{t("alreadyConnected")}</Callout>}
    </StepFrame>
  );
}
