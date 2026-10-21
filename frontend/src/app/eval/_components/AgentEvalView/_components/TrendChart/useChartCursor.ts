import { useRef, useState, type KeyboardEvent } from "react";

export interface ChartCursor {
  /** Run whose tooltip is shown (hovered or focused), null when hidden. */
  active: number | null;
  /** The one run column in the Tab order (roving tabindex); starts on the newest run. */
  tabStop: number;
  setRef: (index: number) => (el: HTMLButtonElement | null) => void;
  /** Event handlers to spread on run column `index`. */
  bind: (index: number) => {
    onMouseEnter: () => void;
    onMouseLeave: () => void;
    onFocus: () => void;
    onBlur: () => void;
    onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => void;
  };
}

/** Pointer and keyboard cursor over `count` run columns: hover or focus shows a run, ←/→
 *  move to the neighbour, Home/End jump to the ends, Escape hides the tooltip. */
export function useChartCursor(count: number): ChartCursor {
  const [shown, setShown] = useState<number | null>(null);
  const [stop, setStop] = useState<number | null>(null);
  const refs = useRef<(HTMLButtonElement | null)[]>([]);

  // Runs can shrink under the cursor (period switch): clamp instead of syncing in an effect.
  const active = shown != null && shown < count ? shown : null;
  const tabStop = stop != null && stop < count ? stop : Math.max(0, count - 1);

  const move = (to: number) => refs.current[Math.min(count - 1, Math.max(0, to))]?.focus();

  return {
    active,
    tabStop,
    setRef: (index) => (el) => {
      refs.current[index] = el;
    },
    bind: (index) => ({
      onMouseEnter: () => setShown(index),
      onMouseLeave: () => setShown(null),
      onFocus: () => {
        setShown(index);
        setStop(index);
      },
      onBlur: () => setShown(null),
      onKeyDown: (e) => {
        const targets: Record<string, number> = { ArrowLeft: index - 1, ArrowRight: index + 1, Home: 0, End: count - 1 };
        const target = targets[e.key];
        if (target != null) {
          e.preventDefault();
          move(target);
        } else if (e.key === "Escape") {
          setShown(null);
        }
      },
    }),
  };
}
