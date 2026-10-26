/* RemovePluginDialog — typed-confirm removal of a plugin (FR17, NFR5, A11). Names what goes with it:
   the counts of the row plus the GitHub Action installations of the plugin's agents. The vendored
   Modal provides role="dialog"; Escape, the focus trap and focus return to the opener come from
   useDialogFocus, which focuses the X first, so the input takes focus explicitly. A name mismatch
   (422 confirmation_mismatch, quiet in the hook) shows inline; any other failure toasts through the
   hook's meta and the dialog stays open. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal, TextInput } from "@devdigest/ui";
import { PLUGIN_REMOVE_MISMATCH_CODE } from "@devdigest/shared/constants/plugins";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useAgents, useRemovePlugin, useWorkspaceCiInstallations } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import type { InstalledPlugin } from "@/lib/types";
import { pluginCiInstallationCount } from "./helpers";
import { s } from "./styles";

/** The server's own message for a mismatched name; every other error is not shown here. */
function mismatchMessage(error: unknown): string | null {
  if (error instanceof ApiError && error.code === PLUGIN_REMOVE_MISMATCH_CODE) return error.message;
  return null;
}

export function RemovePluginDialog({ plugin, onClose }: { plugin: InstalledPlugin; onClose: () => void }) {
  const t = useTranslations("integrationsPlugins.dialog");
  const toast = useToast();
  const agents = useAgents();
  const installations = useWorkspaceCiInstallations();
  const remove = useRemovePlugin();
  const ref = useDialogFocus(onClose);
  const titleId = React.useId();
  const inputId = React.useId();
  const reasonId = React.useId();
  const [typed, setTyped] = React.useState("");

  // Declared after useDialogFocus, so this runs after it moved focus to the X button.
  React.useEffect(() => {
    const root = ref.current;
    root?.querySelector('[role="dialog"]')?.setAttribute("aria-labelledby", titleId);
    root?.querySelector("input")?.focus();
  }, [ref, titleId]);

  const matches = typed === plugin.name;
  const canConfirm = matches && !remove.isPending;
  const mismatch = mismatchMessage(remove.error);
  const ciCount =
    agents.data && installations.data
      ? pluginCiInstallationCount(plugin.id, agents.data, installations.data)
      : null;

  const confirm = async () => {
    if (!canConfirm) return;
    try {
      await remove.mutateAsync({ id: plugin.id, confirm: typed });
      toast.success(t("done", { name: plugin.name }));
      onClose();
    } catch {
      // A name mismatch shows in the dialog and anything else toasted; the dialog stays open.
    }
  };

  const edit = (value: string) => {
    if (remove.isError) remove.reset();
    setTyped(value);
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    void confirm();
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={520}
        title={<span id={titleId}>{t("title", { name: plugin.name })}</span>}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={onClose} disabled={remove.isPending}>
              {t("cancel")}
            </Button>
            <Button
              type="button"
              kind="danger"
              onClick={() => void confirm()}
              disabled={!canConfirm}
              aria-describedby={matches ? undefined : reasonId}
            >
              {remove.isPending ? t("removing") : t("confirm")}
            </Button>
          </div>
        }
      >
        <form onSubmit={submit} noValidate style={s.body}>
          <p style={s.note}>{t("intro")}</p>
          <div>
            <div style={s.heading}>{t("deletedHeading")}</div>
            <ul style={s.list}>
              <li>{t("deleted.agents", { count: plugin.counts.agents })}</li>
              <li>{t("deleted.skills", { count: plugin.counts.skills })}</li>
              <li>{t("deleted.evalCases", { count: plugin.counts.eval_cases })}</li>
              <li>{t("deleted.learnings", { count: plugin.counts.learnings })}</li>
              <li>{ciCount === null ? t("deleted.ciUnknown") : t("deleted.ci", { count: ciCount })}</li>
            </ul>
          </div>
          <div>
            <label htmlFor={inputId} style={s.label}>
              {t("confirmLabel", { name: plugin.name })}
            </label>
            <TextInput
              id={inputId}
              mono
              value={typed}
              onChange={edit}
              readOnly={remove.isPending}
              autoComplete="off"
              spellCheck={false}
            />
            {!matches && (
              <div id={reasonId} style={s.reason}>
                {t("reasonMismatch")}
              </div>
            )}
          </div>
          {mismatch && (
            <div role="alert" style={s.error}>
              {mismatch}
            </div>
          )}
        </form>
      </Modal>
    </div>
  );
}
