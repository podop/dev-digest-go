/* ResetHistoryDialog — typed-confirm reset of the run history (FR19, FR20, NFR5, D22).
   The vendored Modal provides role="dialog"; Escape, the focus trap and focus return come
   from useDialogFocus, which focuses the X first, so the input takes focus explicitly (A8).
   409 run_active and 422 refusals show inside the dialog; any other failure toasts through
   the hook's meta. The hook refetches everything the deleted rows fed (A9). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Modal, TextInput } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useResetHistory, useWorkspaceStats } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { s } from "./styles";

/** Refusals the dialog shows itself (the hook keeps them out of the toast). */
function refusalMessage(error: unknown): string | null {
  if (error instanceof ApiError && (error.status === 409 || error.status === 422)) return error.message;
  return null;
}

export function ResetHistoryDialog({ workspaceName, onClose }: { workspaceName: string; onClose: () => void }) {
  const t = useTranslations("workspaceDanger.dialog");
  const toast = useToast();
  const stats = useWorkspaceStats();
  const reset = useResetHistory();
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

  const matches = typed === workspaceName;
  const canConfirm = matches && !reset.isPending;
  const refusal = refusalMessage(reset.error);

  const confirm = async () => {
    if (!canConfirm) return;
    try {
      const result = await reset.mutateAsync(typed);
      toast.success(
        t("done", {
          runs: result.deleted.agent_runs,
          findings: result.deleted.findings,
          learnings: result.deleted.learnings,
        }),
      );
      onClose();
    } catch {
      // A refusal shows in the dialog and anything else toasted; the dialog stays open.
    }
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    void confirm();
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={520}
        title={<span id={titleId}>{t("title")}</span>}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={onClose} disabled={reset.isPending}>
              {t("cancel")}
            </Button>
            <Button
              type="button"
              kind="danger"
              onClick={() => void confirm()}
              disabled={!canConfirm}
              aria-describedby={matches ? undefined : reasonId}
            >
              {reset.isPending ? t("resetting") : t("confirm")}
            </Button>
          </div>
        }
      >
        <form onSubmit={submit} noValidate style={s.body}>
          <p style={s.note}>{t("intro")}</p>
          <div>
            <div style={s.heading}>{t("deletedHeading")}</div>
            <ul style={s.list}>
              <li>
                {stats.data ? t("deleted.runs", { count: stats.data.runs }) : t("deleted.runsUnknown")}
              </li>
              <li>
                {stats.data
                  ? t("deleted.learnings", { count: stats.data.learnings })
                  : t("deleted.learningsUnknown")}
              </li>
              <li>{t("deleted.other")}</li>
            </ul>
          </div>
          <div>
            <div style={s.heading}>{t("keptHeading")}</div>
            <ul style={s.list}>
              <li>{t("kept.config")}</li>
              <li>{t("kept.pulls")}</li>
            </ul>
          </div>
          <p style={s.note}>{t("exportNote")}</p>
          <p style={s.note}>{t("spendNote")}</p>
          <div>
            <label htmlFor={inputId} style={s.label}>
              {t("confirmLabel", { name: workspaceName })}
            </label>
            <TextInput
              id={inputId}
              mono
              value={typed}
              onChange={setTyped}
              readOnly={reset.isPending}
              autoComplete="off"
              spellCheck={false}
            />
            {!matches && (
              <div id={reasonId} style={s.reason}>
                {t("reasonMismatch")}
              </div>
            )}
          </div>
          {refusal && (
            <div role="alert" style={s.error}>
              {refusal}
            </div>
          )}
        </form>
      </Modal>
    </div>
  );
}
