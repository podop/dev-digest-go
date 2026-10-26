/* Field — a labelled form row of the connection dialog (NFR12): the label is bound to the control, the hint and
   the error are linked with aria-describedby / aria-invalid. */
import type React from "react";
import { s } from "./styles";

/** Props that tie an input to the hint and error text rendered by `Field` with the same `id`. */
export function fieldAria(id: string, error: string | undefined, hint?: boolean) {
  const described = [hint ? `${id}-hint` : null, error ? `${id}-error` : null].filter(Boolean).join(" ");
  return {
    id,
    "aria-invalid": error ? (true as const) : undefined,
    "aria-describedby": described === "" ? undefined : described,
  };
}

export function Field({
  id,
  label,
  aside,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  /** Right of the label (the "Create a token" link). */
  aside?: React.ReactNode;
  hint?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div style={s.field}>
      <div style={s.labelRow}>
        <label htmlFor={id} style={s.label}>
          {label}
        </label>
        <span style={s.spacer} />
        {aside}
      </div>
      {children}
      {hint && (
        <div id={`${id}-hint`} style={s.hint}>
          {hint}
        </div>
      )}
      {error && (
        <div id={`${id}-error`} style={s.error}>
          {error}
        </div>
      )}
    </div>
  );
}
