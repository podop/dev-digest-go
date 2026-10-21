/* BlastGraph — accessible SVG with the nodes of the three columns and a legend; empty state. */
import { describe, it, expect, afterEach } from "vitest";
import type { DownstreamImpact } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { BlastGraph } from "./BlastGraph";
import { GRAPH_MAX_SYMBOLS } from "./helpers";

const DOWNSTREAM: DownstreamImpact[] = [
  {
    symbol: "rateLimit",
    callers: [{ name: "publicRouter", file: "src/router.ts", line: 23 }],
    endpoints_affected: ["GET /public"],
    crons_affected: ["nightly-sweep"],
  },
];

afterEach(cleanup);

describe("BlastGraph", () => {
  it("draws symbol, caller, endpoint and cron nodes with a legend", () => {
    renderWithProviders(<BlastGraph downstream={DOWNSTREAM} />);

    const svg = screen.getByRole("img", { name: "Blast radius graph" });
    for (const label of ["rateLimit()", "publicRouter()", "GET /public", "nightly-sweep"]) {
      // The label is drawn as <text> and repeated as the node's <title> tooltip.
      expect(within(svg).getAllByText(label).length).toBeGreaterThan(0);
    }
    const legend = screen.getByRole("list", { name: "Legend" });
    expect(within(legend).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Changed symbol",
      "Caller",
      "Endpoint",
      "Cron job",
    ]);
  });

  it("caps a big graph and points to the tree for the rest", () => {
    const many: DownstreamImpact[] = Array.from({ length: GRAPH_MAX_SYMBOLS + 2 }, (_, i) => ({
      symbol: `sym${i}`,
      callers: [{ name: `caller${i}`, file: `src/c${i}.ts`, line: 1 }],
      endpoints_affected: [],
      crons_affected: [],
    }));
    renderWithProviders(<BlastGraph downstream={many} />);
    const svg = screen.getByRole("img", { name: "Blast radius graph" });
    expect(within(svg).getAllByText(`sym${GRAPH_MAX_SYMBOLS - 1}()`).length).toBeGreaterThan(0);
    expect(within(svg).queryByText(`sym${GRAPH_MAX_SYMBOLS}()`)).toBeNull();
    expect(screen.getByText("+2 more symbols — see tree view")).toBeInTheDocument();
  });

  it("shows the empty text when there is nothing to draw", () => {
    renderWithProviders(<BlastGraph downstream={[]} />);
    expect(screen.getByText("No downstream callers to graph.")).toBeInTheDocument();
    expect(screen.queryByRole("img")).toBeNull();
  });
});
