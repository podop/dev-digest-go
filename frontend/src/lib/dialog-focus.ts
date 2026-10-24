/* dialog-focus — keyboard handling for a dialog the vendored Modal does not provide:
   Escape closes (unless a Select already used it), Tab stays inside the dialog, and focus
   moves in on open and back to the opener on close (NFR10). Shared by the Export wizard and
   the Settings/Setup dialogs. */
"use client";

import React from "react";

/** Tab-stops inside the dialog (focus trap, NFR10). */
export const FOCUSABLE_SELECTOR =
  'a[href], button, input, select, textarea, [role="combobox"], [tabindex]:not([tabindex="-1"])';

/** The visible tab stops inside a dialog, in DOM order. */
export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (el) => !el.hasAttribute("disabled") && el.getAttribute("aria-hidden") !== "true",
  );
}

/** Where Tab should go to stay inside the dialog; null = let the browser move focus. */
export function wrapTarget(
  stops: readonly HTMLElement[],
  active: Element | null,
  shift: boolean,
  inside: boolean,
): HTMLElement | null {
  const first = stops[0];
  const last = stops[stops.length - 1];
  if (!first || !last) return null;
  if (!inside) return first;
  if (shift && active === first) return last;
  if (!shift && active === last) return first;
  return null;
}

/** Escape closes (unless a Select already used it), Tab stays inside the dialog, and focus
 *  moves in on open and back to the opener on close (NFR10). Returns the wrapper's ref. */
export function useDialogFocus(onClose: () => void) {
  const ref = React.useRef<HTMLDivElement>(null);
  const closeRef = React.useRef(onClose);
  closeRef.current = onClose;

  React.useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const dialog = () => ref.current?.querySelector<HTMLElement>('[role="dialog"]') ?? null;
    focusableIn(dialog() ?? document.body)[0]?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      if (e.key === "Escape") {
        closeRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const root = dialog();
      if (!root) return;
      const stops = focusableIn(root);
      const target = wrapTarget(stops, document.activeElement, e.shiftKey, root.contains(document.activeElement));
      if (target) {
        e.preventDefault();
        target.focus();
      } else if (stops.length === 0) {
        e.preventDefault();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      opener?.focus();
    };
  }, []);

  return ref;
}
