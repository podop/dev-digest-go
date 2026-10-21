/* useSelectListbox — all stateful orchestration for Select: open/active state,
   keyboard (closed + open), type-ahead, outside-close and popup positioning.
   Select.tsx stays declarative: it only renders what this hook returns. */
"use client";

import React from "react";
import { moveActive, placePopup, typeaheadIndex, type NormalizedOption, type PopupPosition } from "./helpers";

const TYPEAHEAD_MS = 500;

function isPrintable(e: React.KeyboardEvent): boolean {
  return e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey;
}

export function useSelectListbox<V extends string>({
  options,
  value,
  onChange,
  disabled,
}: {
  options: NormalizedOption<V>[];
  value: V;
  onChange?: (v: V) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = React.useState(false);
  const [activeIndex, setActiveIndex] = React.useState(-1);
  const [popupPosition, setPopupPosition] = React.useState<PopupPosition | null>(null);
  const triggerRef = React.useRef<HTMLDivElement>(null);
  const listRef = React.useRef<HTMLDivElement>(null);
  const typeahead = React.useRef<{ buffer: string; timer: ReturnType<typeof setTimeout> | null }>({
    buffer: "",
    timer: null,
  });
  const listboxId = React.useId();

  const selectedIndex = options.findIndex((o) => o.value === value);
  const current = options[selectedIndex];

  const close = React.useCallback(() => setOpen(false), []);

  const openAt = React.useCallback(
    (index: number) => {
      if (disabled) return;
      setActiveIndex(options.length > 0 ? Math.max(0, Math.min(index, options.length - 1)) : -1);
      setOpen(true);
    },
    [disabled, options.length],
  );

  const toggle = React.useCallback(() => {
    if (disabled) return;
    if (open) {
      setOpen(false);
      return;
    }
    openAt(selectedIndex >= 0 ? selectedIndex : 0);
  }, [disabled, open, openAt, selectedIndex]);

  const pick = React.useCallback(
    (index: number) => {
      const opt = options[index];
      if (opt) onChange?.(opt.value);
      setOpen(false);
    },
    [options, onChange],
  );

  const runTypeahead = React.useCallback(
    (char: string) => {
      const state = typeahead.current;
      if (state.timer) clearTimeout(state.timer);
      state.buffer += char.toLowerCase();
      const buffer = state.buffer;
      state.timer = setTimeout(() => {
        state.buffer = "";
        state.timer = null;
      }, TYPEAHEAD_MS);

      const labels = options.map((o) => o.label);
      const found = typeaheadIndex(labels, buffer, activeIndex);
      if (found >= 0) {
        if (open) setActiveIndex(found);
        else openAt(found);
      } else if (!open) {
        openAt(selectedIndex >= 0 ? selectedIndex : 0);
      }
    },
    [options, open, activeIndex, openAt, selectedIndex],
  );

  const triggerKeyDown = React.useCallback(
    (e: React.KeyboardEvent) => {
      if (disabled) return;
      const { key } = e;
      if (!open) {
        if (key === "ArrowDown" || key === "ArrowUp" || key === "Enter" || key === " ") {
          e.preventDefault();
          openAt(selectedIndex >= 0 ? selectedIndex : 0);
          return;
        }
        if (key === "Home") {
          e.preventDefault();
          openAt(0);
          return;
        }
        if (key === "End") {
          e.preventDefault();
          openAt(options.length - 1);
          return;
        }
        if (isPrintable(e)) {
          e.preventDefault();
          runTypeahead(key);
        }
        return;
      }
      if (key === "ArrowDown") {
        e.preventDefault();
        setActiveIndex((i) => moveActive("ArrowDown", i, options.length));
      } else if (key === "ArrowUp") {
        e.preventDefault();
        setActiveIndex((i) => moveActive("ArrowUp", i, options.length));
      } else if (key === "Home") {
        e.preventDefault();
        setActiveIndex(0);
      } else if (key === "End") {
        e.preventDefault();
        setActiveIndex(options.length - 1);
      } else if (key === "Enter" || key === " ") {
        e.preventDefault();
        pick(activeIndex);
      } else if (key === "Escape") {
        e.preventDefault();
        close();
      } else if (key === "Tab") {
        // No preventDefault: close, keep the value, let focus move on.
        close();
      } else if (isPrintable(e)) {
        e.preventDefault();
        runTypeahead(key);
      }
    },
    [disabled, open, options.length, selectedIndex, activeIndex, openAt, pick, close, runTypeahead],
  );

  // Outside mousedown closes; checks both the trigger and the portaled list, so a
  // mousedown on an option (which itself preventDefault's) never reaches here first.
  React.useEffect(() => {
    if (!open) return;
    const onMouseDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (triggerRef.current?.contains(target)) return;
      if (listRef.current?.contains(target)) return;
      close();
    };
    document.addEventListener("mousedown", onMouseDown);
    return () => document.removeEventListener("mousedown", onMouseDown);
  }, [open, close]);

  // Position the popup while open; recompute on resize/scroll (ignore scroll inside the list itself).
  React.useLayoutEffect(() => {
    if (!open) {
      setPopupPosition(null);
      return;
    }
    const update = () => {
      const trigger = triggerRef.current;
      if (!trigger) return;
      const rect = trigger.getBoundingClientRect();
      const listH = listRef.current?.scrollHeight ?? 0;
      setPopupPosition(placePopup(rect, window.innerHeight, listH));
    };
    update();
    window.addEventListener("resize", update);
    const onScroll = (e: Event) => {
      if (listRef.current?.contains(e.target as Node)) return;
      update();
    };
    document.addEventListener("scroll", onScroll, true);
    return () => {
      window.removeEventListener("resize", update);
      document.removeEventListener("scroll", onScroll, true);
    };
  }, [open]);

  // Keep the active option visible as it changes.
  React.useEffect(() => {
    if (!open || activeIndex < 0) return;
    const el = listRef.current?.children[activeIndex] as HTMLElement | undefined;
    el?.scrollIntoView?.({ block: "nearest" });
  }, [open, activeIndex]);

  React.useEffect(
    () => () => {
      if (typeahead.current.timer) clearTimeout(typeahead.current.timer);
    },
    [],
  );

  return {
    open,
    activeIndex,
    listboxId,
    triggerRef,
    listRef,
    popupPosition,
    currentLabel: current?.label ?? value,
    triggerKeyDown,
    toggle,
    close,
    pick,
    setActive: setActiveIndex,
  };
}
