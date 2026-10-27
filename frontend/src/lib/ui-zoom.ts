/* The app scales its whole UI with `zoom` on <html> (globals.css --ui-zoom). Under zoom,
   getBoundingClientRect() and window.innerWidth/innerHeight are in screen px, while a
   position: fixed element inside <html> is laid out in zoomed CSS px — so a popup placed
   from a trigger's rect must divide by the zoom first. */

/** The current root zoom (1 where `zoom` is unsupported or unset, e.g. jsdom). */
export function rootZoom(): number {
  if (typeof window === "undefined") return 1;
  const z = Number.parseFloat(getComputedStyle(document.documentElement).zoom);
  return Number.isFinite(z) && z > 0 ? z : 1;
}

export interface CssRect {
  top: number;
  bottom: number;
  left: number;
  right: number;
  width: number;
  height: number;
}

/** An element's viewport rect in the CSS px a position: fixed sibling uses. */
export function cssRect(el: Element): CssRect {
  const r = el.getBoundingClientRect();
  const z = rootZoom();
  return { top: r.top / z, bottom: r.bottom / z, left: r.left / z, right: r.right / z, width: r.width / z, height: r.height / z };
}

/** The viewport size in the same CSS px. */
export function cssViewport(): { width: number; height: number } {
  const z = rootZoom();
  return { width: window.innerWidth / z, height: window.innerHeight / z };
}
