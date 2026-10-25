/* WorkspaceStats — six tiles in FR12 order, no digits while loading, "—" + Retry on failure (AC12, EC15),
   and pseudo-message coverage; real hooks over a stubbed API. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { WorkspaceStats } from "./WorkspaceStats";

afterEach(cleanup);

const STATS = { agents: 4, skills: 12, eval_cases: 31, learnings: 0, repos: 3, runs: 1280 };
const LABELS = ["Agents", "Skills", "Eval cases", "Learnings", "Repositories", "Runs"];
const VALUES = ["4", "12", "31", "0", "3", "1,280"];

const terms = () => screen.getAllByRole("term").map((el) => el.textContent);
const cellOf = (label: string) => (screen.getByText(label).closest("div") as HTMLElement).querySelector("dd");

describe("WorkspaceStats", () => {
  it("shows six tiles in FR12 order, digit-free while loading", async () => {
    mockFetch({ "GET /workspace/stats": STATS });
    renderWithProviders(<WorkspaceStats />);

    const loading = screen.getByRole("region", { name: "Loading workspace statistics…" });
    expect(terms()).toEqual(LABELS);
    expect(loading).toHaveAttribute("aria-busy", "true");
    expect(loading.textContent).not.toMatch(/\d/);
    expect(loading.textContent).not.toContain("—");

    const group = await screen.findByRole("region", { name: "Workspace statistics" });
    expect(group).toHaveAttribute("aria-busy", "false");
    expect(terms()).toEqual(LABELS);
    expect(within(group).getAllByRole("definition").map((el) => el.textContent)).toEqual(VALUES);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(cellOf("Learnings")).toHaveTextContent(/^0$/);
  });

  it("shows — in every tile with Retry on failure, and Retry loads the counts", async () => {
    let calls = 0;
    const api = mockFetch({
      "GET /workspace/stats": () =>
        ++calls === 1 ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : STATS,
    });
    const { user } = renderWithProviders(<WorkspaceStats />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the workspace statistics.");
    expect(terms()).toEqual(LABELS);
    const cells = screen.getAllByRole("definition");
    expect(cells).toHaveLength(6);
    for (const cell of cells) expect(cell).toHaveTextContent(/^—$/);
    expect(screen.getByRole("region", { name: "Workspace statistics" }).textContent).not.toMatch(/\d/);

    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(screen.getAllByRole("definition").map((el) => el.textContent)).toEqual(VALUES),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(api.requests("GET", "/workspace/stats")).toHaveLength(2);
  });

  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({ "GET /workspace/stats": STATS });
    const { container } = renderWithProviders(<WorkspaceStats />, { messages: pseudoMessages() });
    await screen.findByRole("region", { name: "⟦workspaceStats.groupLabel⟧" });

    expect(screen.getByText("⟦workspaceStats.tiles.evalCases⟧")).toBeInTheDocument();
    expect(strayText(container, VALUES)).toEqual([]);
  });

  it("takes the loading and error copy from the message files too", async () => {
    mockFetch({ "GET /workspace/stats": () => jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    const { container } = renderWithProviders(<WorkspaceStats />, { messages: pseudoMessages() });
    expect(screen.getByRole("region", { name: "⟦workspaceStats.loading⟧" })).toBeInTheDocument();
    await screen.findByText("⟦workspaceStats.loadError⟧");

    expect(screen.getAllByText("⟦workspaceStats.unavailable⟧")).toHaveLength(6);
    expect(strayText(container)).toEqual([]);
  });
});
