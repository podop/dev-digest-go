import type { OnboardingEdge, OnboardingNode, OnboardingNodeKind } from "@devdigest/shared";
import { BOX_H, BOX_W, GAP_X, GAP_Y, LABEL_CHARS, MIN_NODES, PAD } from "./constants";

export interface TextPart {
  text: string;
  code: boolean;
}

/** Splits `text` on `inline code` spans. Anything else (including a lone backtick) stays plain text. */
export function splitInlineCode(text: string): TextPart[] {
  const parts: TextPart[] = [];
  const re = /`([^`\n]+)`/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) parts.push({ text: text.slice(last, m.index), code: false });
    parts.push({ text: m[1] ?? "", code: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), code: false });
  return parts;
}

export interface DiagramBox {
  id: string;
  label: string;
  /** Label cut to fit the box; the full label is the tooltip. */
  shortLabel: string;
  kind: OnboardingNodeKind;
  x: number;
  y: number;
}

export interface DiagramArrow {
  key: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
}

export interface DiagramLayout {
  width: number;
  height: number;
  boxes: DiagramBox[];
  arrows: DiagramArrow[];
}

export function shortenLabel(label: string): string {
  return label.length > LABEL_CHARS ? `${label.slice(0, LABEL_CHARS - 1)}…` : label;
}

/** Layer of each node = longest path from a root. Back edges (cycles) are ignored for layering. */
function layerOf(ids: readonly string[], edges: readonly OnboardingEdge[]): Map<string, number> {
  const out = new Map<string, string[]>(ids.map((id) => [id, []]));
  for (const e of edges) out.get(e.from)?.push(e.to);

  // Keep only forward edges: an edge into a node still on the DFS stack closes a cycle.
  const preds = new Map<string, string[]>(ids.map((id) => [id, []]));
  const state = new Map<string, 1 | 2>();
  const visit = (id: string): void => {
    state.set(id, 1);
    for (const to of out.get(id) ?? []) {
      if (state.get(to) === 1) continue;
      preds.get(to)?.push(id);
      if (!state.has(to)) visit(to);
    }
    state.set(id, 2);
  };
  for (const id of ids) if (!state.has(id)) visit(id);

  // The remaining graph is acyclic, so the memoised recursion terminates.
  const layer = new Map<string, number>();
  const depth = (id: string): number => {
    const known = layer.get(id);
    if (known !== undefined) return known;
    const d = Math.max(-1, ...(preds.get(id) ?? []).map(depth)) + 1;
    layer.set(id, d);
    return d;
  };
  for (const id of ids) depth(id);
  return layer;
}

/**
 * Pure layout of the architecture diagram: fixed-size boxes in columns by layer, straight arrows.
 * Duplicate node ids and edges to unknown ids are ignored; fewer than 2 usable nodes → null
 * (the caller hides the diagram area).
 */
export function layoutDiagram(
  nodes: readonly OnboardingNode[],
  edges: readonly OnboardingEdge[],
): DiagramLayout | null {
  const unique: OnboardingNode[] = [];
  const known = new Set<string>();
  for (const n of nodes) {
    if (known.has(n.id)) continue;
    known.add(n.id);
    unique.push(n);
  }
  if (unique.length < MIN_NODES) return null;

  const valid = edges.filter((e) => e.from !== e.to && known.has(e.from) && known.has(e.to));
  const layers = layerOf(
    unique.map((n) => n.id),
    valid,
  );

  const rowsPerLayer = new Map<number, number>();
  const pos = new Map<string, { layer: number; x: number; y: number }>();
  const boxes: DiagramBox[] = unique.map((n) => {
    const layer = layers.get(n.id) ?? 0;
    const row = rowsPerLayer.get(layer) ?? 0;
    rowsPerLayer.set(layer, row + 1);
    const x = PAD + layer * (BOX_W + GAP_X);
    const y = PAD + row * (BOX_H + GAP_Y);
    pos.set(n.id, { layer, x, y });
    return { id: n.id, label: n.label, shortLabel: shortenLabel(n.label), kind: n.kind, x, y };
  });

  const arrows: DiagramArrow[] = [];
  const seen = new Set<string>();
  for (const e of valid) {
    const key = `${e.from}->${e.to}`;
    const a = pos.get(e.from);
    const b = pos.get(e.to);
    if (seen.has(key) || !a || !b) continue;
    seen.add(key);
    const midY = BOX_H / 2;
    if (b.layer > a.layer) arrows.push({ key, x1: a.x + BOX_W, y1: a.y + midY, x2: b.x, y2: b.y + midY });
    else if (b.layer < a.layer) arrows.push({ key, x1: a.x, y1: a.y + midY, x2: b.x + BOX_W, y2: b.y + midY });
    else if (b.y > a.y) arrows.push({ key, x1: a.x + BOX_W / 2, y1: a.y + BOX_H, x2: b.x + BOX_W / 2, y2: b.y });
    else arrows.push({ key, x1: a.x + BOX_W / 2, y1: a.y, x2: b.x + BOX_W / 2, y2: b.y + BOX_H });
  }

  const maxLayer = Math.max(...[...pos.values()].map((p) => p.layer));
  const maxRows = Math.max(...rowsPerLayer.values());
  return {
    width: PAD * 2 + (maxLayer + 1) * BOX_W + maxLayer * GAP_X,
    height: PAD * 2 + maxRows * BOX_H + (maxRows - 1) * GAP_Y,
    boxes,
    arrows,
  };
}
