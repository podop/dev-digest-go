/* test/pseudo.ts — AC34 helper: with `pseudoMessages()` every string from a message
   file renders as a ⟦key⟧ marker, so any other visible text is either data (listed in
   `allowed`) or a hard-coded string. */

const MARKER = /⟦[^⟧]*⟧/g;

/** Visible text nodes under `root` that are neither message markers nor allowed data.
 *  `allowed` entries match a text node exactly (string) or by pattern (RegExp). */
export function strayText(root: HTMLElement, allowed: readonly (string | RegExp)[] = []): string[] {
  const out: string[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = (node.textContent ?? "").replace(MARKER, "").trim();
    if (!text) continue;
    const isData = allowed.some((a) => (typeof a === "string" ? a === node.textContent?.trim() : a.test(node.textContent ?? "")));
    if (!isData) out.push(text);
  }
  return out;
}
