/* BlastGraph layout — pure: three columns (changed symbol → caller → endpoint /
   cron) from the response. The API gives endpoints/crons per symbol, not per
   caller, so they hang off the symbol (dashed), never off a caller. */
import type { DownstreamImpact } from "@devdigest/shared";
import { callerNodeLabel, callerSymbolLabel } from "../../helpers";

export type GraphNodeKind = "symbol" | "caller" | "endpoint" | "cron";

export const NODE_WIDTH = 200;
export const NODE_HEIGHT = 26;
export const ROW_GAP = 10;
export const COLUMN_GAP = 70;
const LABEL_MAX = 30;
/** The graph draws the top-ranked symbols only (the server orders groups by rank); the tree lists all. */
export const GRAPH_MAX_SYMBOLS = 8;

export interface GraphNode {
  id: string;
  kind: GraphNodeKind;
  /** Shortened for the drawing; `title` keeps the full text. */
  label: string;
  title: string;
  x: number;
  y: number;
}

export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  /** symbol → endpoint/cron edges skip the caller column. */
  dashed: boolean;
}

export interface GraphLayout {
  width: number;
  height: number;
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Symbol groups left out of the drawing by GRAPH_MAX_SYMBOLS. */
  hiddenSymbols: number;
}

export function shorten(text: string, max = LABEL_MAX): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
}

const COLUMN_OF: Record<GraphNodeKind, number> = { symbol: 0, caller: 1, endpoint: 2, cron: 2 };

/** Only the first GRAPH_MAX_SYMBOLS groups (and their callers/endpoints/crons) are drawn; `hiddenSymbols` counts the rest. Nodes are de-duplicated (a caller reaching two symbols is one node with two edges); an empty layout has no nodes. */
export function graphLayout(downstream: readonly DownstreamImpact[]): GraphLayout {
  const columns: { id: string; kind: GraphNodeKind; label: string; title: string }[][] = [[], [], []];
  const seen = new Set<string>();
  const edges: GraphEdge[] = [];

  const addNode = (id: string, kind: GraphNodeKind, title: string, label = title) => {
    if (seen.has(id)) return;
    seen.add(id);
    columns[COLUMN_OF[kind]]!.push({ id, kind, label, title });
  };
  const addEdge = (from: string, to: string, dashed: boolean) => {
    const id = `${from}->${to}`;
    if (!edges.some((e) => e.id === id)) edges.push({ id, from, to, dashed });
  };

  const shown = downstream.slice(0, GRAPH_MAX_SYMBOLS);
  for (const d of shown) {
    const symbolId = `symbol:${d.symbol}`;
    addNode(symbolId, "symbol", `${d.symbol}()`);
    for (const c of d.callers) {
      const callerId = `caller:${c.file}:${c.name}`;
      const symbol = callerSymbolLabel(c);
      addNode(callerId, "caller", symbol ? `${symbol} — ${c.file}` : c.file, callerNodeLabel(c));
      addEdge(symbolId, callerId, false);
    }
    for (const e of d.endpoints_affected) {
      addNode(`endpoint:${e}`, "endpoint", e);
      addEdge(symbolId, `endpoint:${e}`, true);
    }
    for (const c of d.crons_affected) {
      addNode(`cron:${c}`, "cron", c);
      addEdge(symbolId, `cron:${c}`, true);
    }
  }

  const nodes: GraphNode[] = columns.flatMap((col, ci) =>
    col.map((n, row) => ({
      id: n.id,
      kind: n.kind,
      label: shorten(n.label),
      title: n.title,
      x: ci * (NODE_WIDTH + COLUMN_GAP),
      y: row * (NODE_HEIGHT + ROW_GAP),
    })),
  );
  const rows = Math.max(0, ...columns.map((c) => c.length));
  return {
    width: 3 * NODE_WIDTH + 2 * COLUMN_GAP,
    height: rows === 0 ? 0 : rows * NODE_HEIGHT + (rows - 1) * ROW_GAP,
    nodes,
    edges,
    hiddenSymbols: downstream.length - shown.length,
  };
}
