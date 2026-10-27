/* Select — a themed listbox with the vendored native select's props/look, replacing
   the OS-native <select> popup so both themes stay legible (that popup is drawn by
   the OS and can't be styled — see docs/plans/2026-09-25-themed-select.md).
   ARIA: APG select-only combobox — a focusable `role="combobox"` div drives a
   portaled `role="listbox"`; `aria-activedescendant` tracks the active option. */
"use client";

import React from "react";
import { createPortal } from "react-dom";
import { Icon, type IconName } from "@devdigest/ui";
import { normalizeOptions, type SelectOption } from "./helpers";
import { useSelectListbox } from "./useSelectListbox";
import { s } from "./styles";

export type { SelectOption };

/** Trailing indicator of the trigger: the up/down pair (default), a single down chevron, or none. */
export type SelectChevron = "updown" | "down" | "none";

export function Select<V extends string>({
  value,
  onChange,
  options,
  mono = true,
  "aria-label": ariaLabel,
  disabled,
  formatValue,
  icon,
  chevron = "updown",
  compact,
  id,
}: {
  value: V;
  onChange?: (v: V) => void;
  options: readonly SelectOption<V>[];
  mono?: boolean;
  "aria-label"?: string;
  disabled?: boolean;
  /** Trigger text for the current option's label (e.g. "Sort: Newest"); the list keeps the bare labels. */
  formatValue?: (label: string) => string;
  /** Decorative icon before the label, on the trigger and on every option. */
  icon?: IconName;
  chevron?: SelectChevron;
  /** Design-prototype size (8px 11px, 13px) used by Settings and Setup. */
  compact?: boolean;
  /** Id of the trigger, so a `<label htmlFor>` names it. */
  id?: string;
}) {
  const normalized = React.useMemo(() => normalizeOptions(options), [options]);
  const {
    open,
    activeIndex,
    listboxId,
    triggerRef,
    listRef,
    popupPosition,
    currentLabel,
    triggerKeyDown,
    toggle,
    close,
    pick,
    setActive,
  } = useSelectListbox({ options: normalized, value, onChange, disabled });

  const activeId = open && activeIndex >= 0 ? `${listboxId}-option-${activeIndex}` : undefined;

  return (
    <div
      ref={triggerRef}
      id={id}
      role="combobox"
      tabIndex={disabled ? -1 : 0}
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-controls={listboxId}
      aria-activedescendant={activeId}
      aria-disabled={disabled || undefined}
      aria-label={ariaLabel}
      onClick={toggle}
      onKeyDown={triggerKeyDown}
      onBlur={close}
      className={mono ? "mono" : undefined}
      style={s.trigger(disabled, compact)}
    >
      {icon && <OptionIcon name={icon} testId="select-trigger-icon" />}
      <span style={s.value(!normalized.some((o) => o.value === value), compact)}>{formatValue ? formatValue(currentLabel) : currentLabel}</span>
      {chevron === "updown" && <Icon.ChevronsUpDown size={14} style={s.chevron} />}
      {chevron === "down" && <Icon.ChevronDown size={14} style={s.chevron} />}
      {open &&
        !disabled &&
        createPortal(
          // A generic div (not <ul>) carries no implicit role, so it can freely take
          // role="listbox"; its options manage focus virtually via aria-activedescendant
          // on the combobox, per the APG select-only combobox pattern — they stay out of
          // the tab order on purpose, hence tabIndex={-1} rather than 0.
          <div
            id={listboxId}
            role="listbox"
            ref={listRef}
            onMouseDown={(e) => e.preventDefault()}
            style={s.popup(popupPosition ?? { left: -9999, width: 0, top: -9999, maxHeight: 0 })}
          >
            {normalized.map((o, i) => (
              <div
                key={o.value}
                id={`${listboxId}-option-${i}`}
                role="option"
                tabIndex={-1}
                aria-selected={o.value === value}
                onMouseEnter={() => setActive(i)}
                onClick={(e) => {
                  e.stopPropagation();
                  pick(i);
                }}
                className={mono ? "mono" : undefined}
                style={s.option(i === activeIndex)}
              >
                <Icon.Check size={13} style={s.check(o.value === value)} />
                {icon && <OptionIcon name={icon} testId="select-option-icon" />}
                <span style={s.optionLabel}>{o.label}</span>
              </div>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}

function OptionIcon({ name, testId }: { name: IconName; testId: string }) {
  const Glyph = Icon[name];
  return <Glyph size={14} aria-hidden data-testid={testId} style={s.icon} />;
}
