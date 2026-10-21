/* AgentsOverview — rows with the latest finished run, sparkline and metrics; an agent
   with cases but no run shows "—" metrics. */
import { describe, it, expect, afterEach } from "vitest";
import type { EvalAgentOverview } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { makeRun } from "@/test/eval-fixtures";
import { AgentsOverview } from "./AgentsOverview";

afterEach(cleanup);

const spark = (recall: number | null, i: number) => ({ run_id: `r${i}`, recall, precision: 0.8, citation_accuracy: 0.9 });

const RAN = new Date(2026, 4, 29, 9, 14).toISOString();
const LONG_NAME = "N".repeat(120);

const AGENTS: EvalAgentOverview[] = [
  {
    agent_id: "ag1",
    name: "Security Reviewer",
    model: "gpt-4.1",
    cases_total: 20,
    latest: makeRun({ agent_version: 7, started_at: RAN, cases_passed: 17, cases_total: 20, recall: 0.82, precision: 0.91, citation_accuracy: 0.95 }),
    sparkline: [spark(0.5, 1), spark(0.6, 2), spark(0.82, 3)],
  },
  { agent_id: "ag2", name: "Perf <img src=x onerror=alert(1)>", model: "claude", cases_total: 1, latest: null, sparkline: [] },
  { agent_id: "ag3", name: LONG_NAME, model: "", cases_total: 2, latest: null, sparkline: [spark(0.5, 1)] },
];

describe("AgentsOverview", () => {
  it("renders each agent as one link card with model, last run, sparkline and labelled whole-percent metrics; a never-run agent shows dashes", () => {
    const { container } = renderWithProviders(<AgentsOverview agents={AGENTS} />);
    const cards = screen.getAllByTestId("eval-agent-card");
    expect(cards).toHaveLength(3);

    const first = cards[0]!;
    expect(first).toHaveAttribute("href", "/eval?agent=ag1");
    expect(first).toHaveTextContent("Security Reviewer");
    expect(first).toHaveTextContent("Last run v7 · 2026-05-29 09:14 · 17/20 pass");
    const c1 = within(first);
    expect(c1.getByText("gpt-4.1")).toBeInTheDocument();
    for (const [label, value] of [["RECALL", "82%"], ["PREC", "91%"], ["CITE", "95%"]] as const) {
      expect(c1.getByText(label)).toBeInTheDocument();
      expect(c1.getByText(value)).toBeInTheDocument();
    }
    expect(c1.getByRole("img", { name: "Recall trend" })).toBeInTheDocument();

    const second = within(cards[1]!);
    expect(second.getByText("Never run")).toBeInTheDocument();
    expect(second.getAllByText("—")).toHaveLength(3);
    expect(second.queryByRole("img", { name: "Recall trend" })).not.toBeInTheDocument();
    // Agent names are plain text (NFR7).
    expect(cards[1]).toHaveTextContent("Perf <img src=x onerror=alert(1)>");
    expect(container.querySelector("img")).toBeNull();
  });

  it("AC19: a long name stays on one line with ellipsis and carries the full name as a native tooltip; one sparkline point draws no line; no model, no badge", () => {
    renderWithProviders(<AgentsOverview agents={AGENTS} />);
    const name = screen.getByTitle(LONG_NAME);
    expect(name).toHaveTextContent(LONG_NAME);
    expect(name).toHaveStyle({ whiteSpace: "nowrap", textOverflow: "ellipsis", overflow: "hidden" });
    const card = within(screen.getAllByTestId("eval-agent-card")[2]!);
    expect(card.queryByRole("img", { name: "Recall trend" })).not.toBeInTheDocument();
  });
});
