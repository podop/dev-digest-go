import { describe, it, expect } from "vitest";
import type { DownstreamImpact } from "@devdigest/shared";
import { graphLayout, GRAPH_MAX_SYMBOLS, NODE_HEIGHT, NODE_WIDTH, ROW_GAP, COLUMN_GAP, shorten } from "./helpers";

const DOWNSTREAM: DownstreamImpact[] = [
  {
    symbol: "a",
    callers: [
      { name: "x", file: "src/x.ts", line: 1 },
      { name: "y", file: "src/y.ts", line: 2 },
    ],
    endpoints_affected: ["GET /x"],
    crons_affected: ["nightly"],
  },
  {
    symbol: "b",
    callers: [{ name: "x", file: "src/x.ts", line: 1 }],
    endpoints_affected: ["GET /x"],
    crons_affected: [],
  },
];

describe("graphLayout", () => {
  it("puts symbols, callers and endpoints/crons in three columns and shares nodes between symbols", () => {
    const g = graphLayout(DOWNSTREAM);
    const byKind = (k: string) => g.nodes.filter((n) => n.kind === k);

    expect(byKind("symbol").map((n) => [n.label, n.x])).toEqual([["a()", 0], ["b()", 0]]);
    expect(byKind("caller").map((n) => [n.label, n.x])).toEqual([["x()", NODE_WIDTH + COLUMN_GAP], ["y()", NODE_WIDTH + COLUMN_GAP]]);
    expect(byKind("endpoint")).toHaveLength(1);
    expect(byKind("endpoint")[0]!.x).toBe(2 * (NODE_WIDTH + COLUMN_GAP));
    expect(byKind("cron")[0]!.x).toBe(2 * (NODE_WIDTH + COLUMN_GAP));
    // Rows stack top-down in each column.
    expect(byKind("caller").map((n) => n.y)).toEqual([0, NODE_HEIGHT + ROW_GAP]);
    expect(g.height).toBe(2 * NODE_HEIGHT + ROW_GAP);
  });

  it("links symbol -> caller (solid) and symbol -> endpoint/cron (dashed), each edge once", () => {
    const g = graphLayout(DOWNSTREAM);
    expect(g.edges.filter((e) => !e.dashed).map((e) => e.id)).toEqual([
      "symbol:a->caller:src/x.ts:x",
      "symbol:a->caller:src/y.ts:y",
      "symbol:b->caller:src/x.ts:x",
    ]);
    expect(g.edges.filter((e) => e.dashed).map((e) => e.id)).toEqual([
      "symbol:a->endpoint:GET /x",
      "symbol:a->cron:nightly",
      "symbol:b->endpoint:GET /x",
    ]);
    const ids = new Set(g.nodes.map((n) => n.id));
    expect(g.edges.every((e) => ids.has(e.from) && ids.has(e.to))).toBe(true);
  });

  it("draws only the first GRAPH_MAX_SYMBOLS groups with their callers/endpoints and counts the rest as hidden", () => {
    const many: DownstreamImpact[] = Array.from({ length: GRAPH_MAX_SYMBOLS + 3 }, (_, i) => ({
      symbol: `s${i}`,
      callers: [{ name: `c${i}`, file: `src/c${i}.ts`, line: i + 1 }],
      endpoints_affected: [`GET /e${i}`],
      crons_affected: [],
    }));
    const g = graphLayout(many);
    const symbols = g.nodes.filter((n) => n.kind === "symbol").map((n) => n.label);
    expect(symbols).toEqual(Array.from({ length: GRAPH_MAX_SYMBOLS }, (_, i) => `s${i}()`));
    expect(g.nodes.filter((n) => n.kind === "caller")).toHaveLength(GRAPH_MAX_SYMBOLS);
    expect(g.nodes.filter((n) => n.kind === "endpoint")).toHaveLength(GRAPH_MAX_SYMBOLS);
    expect(g.nodes.some((n) => n.id.includes(`s${GRAPH_MAX_SYMBOLS}`) || n.id.includes(`c${GRAPH_MAX_SYMBOLS}`))).toBe(false);
    expect(g.hiddenSymbols).toBe(3);
    expect(graphLayout(DOWNSTREAM).hiddenSymbols).toBe(0);
  });

  it("labels a file-name caller without () and keeps the full path as its title", () => {
    const g = graphLayout([
      { symbol: "a", callers: [{ name: "x.test.ts", file: "src/x.test.ts", line: 1 }], endpoints_affected: [], crons_affected: [] },
    ]);
    const caller = g.nodes.find((n) => n.kind === "caller")!;
    expect([caller.label, caller.title]).toEqual(["x.test.ts", "src/x.test.ts"]);
  });

  it("is empty without downstream", () => {
    expect(graphLayout([])).toMatchObject({ nodes: [], edges: [], height: 0 });
  });
});

describe("shorten", () => {
  it("cuts long labels with an ellipsis", () => {
    expect(shorten("abcdef", 4)).toBe("abc…");
    expect(shorten("abc", 4)).toBe("abc");
  });
});
