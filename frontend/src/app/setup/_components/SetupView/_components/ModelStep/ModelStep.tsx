"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KEY_PAGE_URLS, KeyField } from "@/components/key-field";
import type { ConnTestProvider, ConnTestResult, SecretsStatus } from "@/lib/types";
import { MODEL_PROVIDERS, type ModelProvider } from "../../constants";
import { initialProvider } from "../../helpers";
import { StepFrame } from "../StepFrame";
import { s } from "./styles";

/** Step 1 (FR23): choose a provider, test its key. Continue needs a passing test of the chosen
 *  provider, or a stored key that no test of this view has failed. */
export function ModelStep({
  secrets,
  onTested,
  onContinue,
}: {
  secrets: SecretsStatus;
  onTested: (provider: ConnTestProvider) => void;
  onContinue?: () => void;
}) {
  const t = useTranslations("setup");
  const [provider, setProvider] = React.useState<ModelProvider>(() => initialProvider(secrets));
  // Last test outcome of this view, per provider; switching provider starts from the stored state (EC12).
  const [outcomes, setOutcomes] = React.useState<Partial<Record<ConnTestProvider, boolean>>>({});

  const outcome = outcomes[provider];
  const ready = outcome ?? secrets[provider];
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
      <fieldset style={s.group}>
        <legend style={s.legend}>{t("model.providerLegend")}</legend>
        <div style={s.options}>
          {MODEL_PROVIDERS.map((p) => (
            <label key={p} style={s.option(p === provider)}>
              <input type="radio" name="setup-provider" value={p} checked={p === provider} onChange={() => setProvider(p)} />
              {t(`model.providers.${p}`)}
            </label>
          ))}
        </div>
      </fieldset>
      <KeyField
        key={provider}
        provider={provider}
        label={t("model.keyLabel", { provider: t(`model.providers.${provider}`) })}
        hint={t("storedLocally")}
        docsHref={KEY_PAGE_URLS[provider]}
        configured={secrets[provider]}
        onResult={onResult}
      />
      {alreadyConnected && (
        <div style={s.connected}>
          <Icon.CheckCircle size={14} />
          {t("alreadyConnected")}
        </div>
      )}
    </StepFrame>
  );
}
