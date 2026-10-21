/* useCaseEditor — state and side effects of the case editor: the draft over the stored case,
   the debounced diff analysis (FR10), the save, the Escape / discard flow (FR14) and focus
   (NFR5). Everything the form shows is derived from the draft; nothing is synced by effect. */
"use client";

import React from "react";
import type { AgentEvalCase } from "@devdigest/shared";
import { useCreateManualEvalCase, useUpdateEvalCase, type EvalCaseOwner } from "@/lib/hooks";
import { DEBOUNCE_MS } from "./constants";
import {
  baseValues,
  buildCreateInput,
  buildPatch,
  isDirty,
  saveError,
  splitPastedDiff,
  validateCase,
  type EditorDraft,
  type EditorValues,
} from "./helpers";

export type InputTab = "diff" | "meta";

/** `value`, once it stopped changing for `ms`. */
function useDebouncedValue<T>(value: T, ms: number): T {
  const [settled, setSettled] = React.useState(value);
  React.useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return settled;
}

export function useCaseEditor({ owner, evalCase, onClose }: { owner: EvalCaseOwner; evalCase: AgentEvalCase | null; onClose: () => void }) {
  const create = useCreateManualEvalCase(owner);
  const update = useUpdateEvalCase();
  const [draft, setDraft] = React.useState<EditorDraft>({});
  const [tab, setTab] = React.useState<InputTab>("diff");
  const [confirmingDiscard, setConfirmingDiscard] = React.useState(false);
  const saving = React.useRef(false);
  const nameId = React.useId();

  const base = React.useMemo(() => baseValues(evalCase), [evalCase]);
  const merged: EditorValues = { ...base, ...draft };

  // The preview and the checks follow the pasted text and the file after a pause, so a 200 000-char paste never blocks typing.
  const diff = useDebouncedValue(merged.diff, DEBOUNCE_MS);
  const file = useDebouncedValue(merged.file, DEBOUNCE_MS);
  const analysis = React.useMemo(() => splitPastedDiff(diff, file), [diff, file]);
  const deferredAnalysis = React.useDeferredValue(analysis);
  const settling = diff !== merged.diff || file !== merged.file;

  // A diff with file headers and no file chosen yet: the first of its files is selected (FR10).
  const values: EditorValues = { ...merged, file: merged.file === "" && analysis.hasHeaders ? (analysis.files[0]?.path ?? "") : merged.file };
  const dirty = isDirty(base, values);
  const validity = validateCase({ ...values, diff }, analysis);

  const mutation = evalCase ? update : create;
  const pending = mutation.isPending;
  const failure = mutation.isError ? saveError(mutation.error) : null;
  const patch = evalCase ? buildPatch(base, values) : null;
  const canSave = validity.ok && !settling && !pending && (evalCase ? patch != null : true);

  const edit = <K extends keyof EditorValues>(field: K, value: EditorValues[K]) => {
    if (mutation.isError) mutation.reset();
    setDraft((d) => ({ ...d, [field]: value }));
  };

  const save = () => {
    if (!canSave || saving.current) return;
    saving.current = true;
    const done = { onSuccess: onClose, onSettled: () => (saving.current = false) };
    if (evalCase && patch) update.mutate({ id: evalCase.id, patch }, done);
    else create.mutate(buildCreateInput(values), done);
  };

  // Cancel, the close button, the backdrop and Escape all end here.
  const requestClose = React.useCallback(() => {
    if (dirty) setConfirmingDiscard(true);
    else onClose();
  }, [dirty, onClose]);

  // The vendored Modal has no Escape handling. A Select closing its list marks the event handled.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (confirmingDiscard) setConfirmingDiscard(false);
      else requestClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [confirmingDiscard, requestClose]);

  // Focus moves to Name on open and back to the opener on close.
  React.useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    document.getElementById(nameId)?.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  }, [nameId]);

  return {
    values,
    validity,
    analysis: deferredAnalysis,
    failure,
    tab,
    setTab,
    edit,
    save,
    canSave,
    pending,
    dirty,
    nameId,
    confirmingDiscard,
    keepEditing: () => setConfirmingDiscard(false),
    requestClose,
  };
}
