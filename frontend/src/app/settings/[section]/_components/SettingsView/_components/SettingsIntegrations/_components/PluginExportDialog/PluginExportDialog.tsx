/* PluginExportDialog — options of "Export workspace as plugin" (FR15, EC15, NFR5, D12).
   The vendored Modal has no Escape or focus handling, so useDialogFocus provides them and
   returns focus to the opener (A8). The card owns the mutation (its button shows "Exporting…"
   too); a 4xx refusal shows here, a 5xx or network failure reads "Export failed — try again".
   The dialog stays open on failure and ignores Escape/Cancel while the request runs. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { PluginExportRequest } from "@devdigest/shared";
import { Button, Checkbox, Modal, TextInput } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useAgents, useSkills } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { SelectionList } from "./_components/SelectionList";
import { buildExportRequest, isInvalidExportName, selectionSize } from "./helpers";
import { s } from "./styles";

interface Props {
  onClose: () => void;
  /** Runs the export; resolves with the saved file's name, rejects with the failure. */
  onExport: (request: PluginExportRequest) => Promise<{ fileName: string }>;
  pending: boolean;
  /** The last export's failure, if any. */
  error: unknown;
}

const NO_IDS: readonly string[] = [];

export function PluginExportDialog({ onClose, onExport, pending, error }: Props) {
  const t = useTranslations("integrationsExport.dialog");
  const toast = useToast();
  const agents = useAgents();
  const skills = useSkills();
  const close = () => {
    if (!pending) onClose();
  };
  const ref = useDialogFocus(close);
  const titleId = React.useId();
  const nameId = React.useId();
  const nameHintId = React.useId();
  const [name, setName] = React.useState("");
  const [excludedAgents, setExcludedAgents] = React.useState<ReadonlySet<string>>(new Set());
  const [excludedSkills, setExcludedSkills] = React.useState<ReadonlySet<string>>(new Set());
  const [includeEvalCases, setIncludeEvalCases] = React.useState(true);
  const [includeLearnings, setIncludeLearnings] = React.useState(false);

  // Declared after useDialogFocus, so this runs after it moved focus to the X button.
  React.useEffect(() => {
    const root = ref.current;
    root?.querySelector('[role="dialog"]')?.setAttribute("aria-labelledby", titleId);
    root?.querySelector("input")?.focus();
  }, [ref, titleId]);

  const agentList = agents.data;
  const skillList = skills.data;
  const loaded = agentList !== undefined && skillList !== undefined;
  const loadFailed = agents.isError || skills.isError;
  const selection = {
    agentIds: agentList?.map((a) => a.id) ?? NO_IDS,
    skillIds: skillList?.map((sk) => sk.id) ?? NO_IDS,
    excludedAgents,
    excludedSkills,
  };
  const nameInvalid = isInvalidExportName(name);
  const nothingSelected = loaded && selectionSize(selection) === 0;
  const canExport = loaded && !nameInvalid && !nothingSelected && !pending;
  const failure = error instanceof ApiError && error.status >= 400 && error.status < 500 ? error.message : t("failed");

  const submit = async () => {
    if (!canExport) return;
    try {
      const { fileName } = await onExport(buildExportRequest(selection, { name, includeEvalCases, includeLearnings }));
      toast.success(t("exported", { fileName }));
      onClose();
    } catch {
      // The failure arrives through `error` and stays visible in the dialog.
    }
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    void submit();
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={540}
        title={<span id={titleId}>{t("title")}</span>}
        onClose={close}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={close} disabled={pending}>
              {t("cancel")}
            </Button>
            <Button type="button" kind="primary" onClick={() => void submit()} disabled={!canExport}>
              {pending ? t("exporting") : t("export")}
            </Button>
          </div>
        }
      >
        <form onSubmit={onSubmit} noValidate style={s.body}>
          <p style={s.note}>{t("intro")}</p>
          <div>
            <label htmlFor={nameId} style={s.label}>
              {t("name.label")}
            </label>
            <TextInput
              id={nameId}
              mono
              value={name}
              onChange={setName}
              placeholder={t("name.placeholder")}
              readOnly={pending}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={nameInvalid}
              aria-describedby={nameHintId}
            />
            <div id={nameHintId} style={nameInvalid ? s.invalid : s.hint}>
              {nameInvalid ? t("name.invalid") : t("name.hint")}
            </div>
          </div>
          {loadFailed && (
            <div role="alert" style={s.error}>
              {t("loadFailed")}
            </div>
          )}
          {!loaded && !loadFailed && <p style={s.note}>{t("loading")}</p>}
          {loaded && (
            <>
              <SelectionList
                heading={t("agents.heading")}
                emptyText={t("agents.empty")}
                items={agentList}
                excluded={excludedAgents}
                onChange={setExcludedAgents}
                disabled={pending}
              />
              <SelectionList
                heading={t("skills.heading")}
                emptyText={t("skills.empty")}
                items={skillList}
                excluded={excludedSkills}
                onChange={setExcludedSkills}
                disabled={pending}
              />
              <p style={s.hint}>{t("skills.note")}</p>
              {nothingSelected && <p style={s.hint}>{t("selection.none")}</p>}
            </>
          )}
          <div style={s.options}>
            <Checkbox checked={includeEvalCases} onChange={setIncludeEvalCases} label={t("options.evalCases")} />
            <Checkbox checked={includeLearnings} onChange={setIncludeLearnings} label={t("options.learnings")} />
          </div>
          {error != null && (
            <div role="alert" style={s.error}>
              {failure}
            </div>
          )}
        </form>
      </Modal>
    </div>
  );
}
