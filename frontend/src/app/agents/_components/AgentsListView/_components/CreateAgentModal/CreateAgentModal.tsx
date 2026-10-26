"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button, Modal, FormField, TextInput, Textarea } from "@devdigest/ui";
import type { Provider } from "@devdigest/shared";
import { ProviderSelect, useProviderUndefined } from "@/components/provider-select";
import { useCreateAgent, useSettings } from "@/lib/hooks";
import { ModelSelectField } from "@/app/agents/_components/ModelSelectField";
import { DEFAULT_AGENT_MODEL, DEFAULT_AGENT_PROVIDER } from "@/lib/model-defaults";
import { MODAL_WIDTH } from "./constants";
import { s } from "./styles";

interface ModelChoice {
  provider: Provider;
  model: string;
}

/** Create-agent modal — name/description/provider/model/system-prompt. */
export function CreateAgentModal({ onClose }: { onClose: () => void }) {
  const t = useTranslations("agents");
  const tp = useTranslations("providerSelect");
  const router = useRouter();
  const create = useCreateAgent();
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const { data: settings } = useSettings();
  // Starting point: the workspace default model when set (FR10), else the built-in agent default.
  // Derived while rendering, so a settings response that arrives after the modal opened still
  // applies; once the user picks something, `picked` wins.
  const base: ModelChoice = settings?.default_model ?? { provider: DEFAULT_AGENT_PROVIDER, model: DEFAULT_AGENT_MODEL };
  const [picked, setPicked] = React.useState<ModelChoice | null>(null);
  const { provider, model } = picked ?? base;
  // A default model on a custom provider that no longer exists: pick another provider to create (FR19).
  const providerUndefined = useProviderUndefined(provider);
  const [systemPrompt, setSystemPrompt] = React.useState(t("create.defaultSystemPrompt"));

  // Model ids are provider-specific (gpt-4.1 vs openai/gpt-4.1), so a provider
  // switch clears the model and the user picks one from the new list (switching back to
  // the starting provider restores the starting model).
  const changeProvider = (next: Provider) => setPicked({ provider: next, model: next === base.provider ? base.model : "" });
  const setModel = (next: string) => setPicked({ provider, model: next });

  const submit = async () => {
    const agent = await create.mutateAsync({
      name: name.trim() || t("create.defaultName"),
      description,
      provider,
      model,
      system_prompt: systemPrompt,
    });
    onClose();
    router.push(`/agents/${agent.id}?tab=config`);
  };

  return (
    <Modal
      width={MODAL_WIDTH}
      title={t("create.title")}
      subtitle={t("create.subtitle")}
      onClose={onClose}
      footer={
        <div style={s.footer}>
          <Button kind="ghost" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button kind="primary" icon="Plus" onClick={submit} disabled={create.isPending || !model || providerUndefined}>
            {create.isPending ? t("create.creating") : t("create.create")}
          </Button>
        </div>
      }
    >
      <div style={s.body}>
        <FormField label={t("create.fields.name")} required>
          <TextInput value={name} onChange={setName} placeholder={t("create.fields.namePlaceholder")} />
        </FormField>
        <FormField label={t("create.fields.description")}>
          <TextInput
            value={description}
            onChange={setDescription}
            placeholder={t("create.fields.descriptionPlaceholder")}
          />
        </FormField>
        <FormField label={t("create.fields.provider")} hint={providerUndefined ? tp("blockedHint") : undefined}>
          <ProviderSelect value={provider} onChange={changeProvider} aria-label={t("create.fields.provider")} />
        </FormField>
        <ModelSelectField provider={provider} value={model} onChange={setModel} />
        <FormField label={t("create.fields.systemPrompt")}>
          <Textarea value={systemPrompt} onChange={setSystemPrompt} rows={6} mono />
        </FormField>
      </div>
    </Modal>
  );
}
