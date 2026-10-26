/* Field + RadioGroup — labelled form pieces of the custom provider dialog (NFR9): every control has a
   label; a field error and hint are linked with aria-describedby / aria-invalid. */
import type React from "react";
import { s } from "./styles";

/** Props that tie an input to its hint and error text rendered by `Field` with the same `id`. */
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
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div style={s.field}>
      <label htmlFor={id} style={s.label}>
        {label}
      </label>
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

export interface RadioOption<V extends string> {
  value: V;
  label: string;
}

/** A native radio group in a fieldset, so the legend names it and arrow keys move between the options. */
export function RadioGroup<V extends string>({
  legend,
  name,
  value,
  options,
  onChange,
  describedBy,
  disabled,
}: {
  legend: string;
  name: string;
  value: V;
  options: readonly RadioOption<V>[];
  onChange: (v: V) => void;
  describedBy?: string;
  disabled?: boolean;
}) {
  return (
    <fieldset style={s.field} aria-describedby={describedBy} disabled={disabled}>
      <legend style={s.label}>{legend}</legend>
      <div style={s.radios}>
        {options.map((o) => (
          <label key={o.value} style={s.radio}>
            <input type="radio" name={name} value={o.value} checked={value === o.value} onChange={() => onChange(o.value)} />
            {o.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
