"use client";

import type React from "react";
import { Icon } from "@devdigest/ui";
import { s } from "./styles";

/** A labelled radio group drawn as prototype Chips; the selected chip carries a check. */
export function ChoiceGroup<V extends string>({
  legend,
  name,
  options,
  value,
  onChange,
  labelOf,
  right,
}: {
  legend: string;
  name: string;
  options: readonly V[];
  value: V;
  onChange: (v: V) => void;
  labelOf: (v: V) => string;
  /** Content at the right of the legend (e.g. a link). */
  right?: React.ReactNode;
}) {
  return (
    <fieldset style={s.group}>
      <legend style={s.legend}>{legend}</legend>
      {right && <div style={s.right}>{right}</div>}
      <div style={s.options}>
        {options.map((o) => (
          <label key={o} style={s.option(o === value)}>
            <input type="radio" name={name} value={o} checked={o === value} onChange={() => onChange(o)} style={s.radio} />
            {o === value ? <Icon.Check size={13} aria-hidden /> : null}
            {labelOf(o)}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
