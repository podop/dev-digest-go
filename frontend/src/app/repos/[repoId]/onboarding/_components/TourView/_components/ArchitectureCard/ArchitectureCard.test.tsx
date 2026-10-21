/* ArchitectureCard — AC16: summary + a diagram with labelled boxes; one node hides the diagram
   and keeps the summary; the summary renders `inline code` as <code>, never as markup. */
import { describe, it, expect, afterEach } from "vitest";
import type { OnboardingArchitecture } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { ArchitectureCard } from "./ArchitectureCard";

afterEach(cleanup);

const arch = (over: Partial<OnboardingArchitecture> = {}): OnboardingArchitecture => ({
  summary: "Requests enter through `src/server.ts` and reach the db.",
  nodes: [
    { id: "client", label: "client", kind: "external" },
    { id: "server", label: "server.ts", kind: "entry" },
    { id: "mw", label: "middleware", kind: "module" },
    { id: "pg", label: "postgres", kind: "store" },
  ],
  edges: [
    { from: "client", to: "server" },
    { from: "server", to: "mw" },
    { from: "mw", to: "pg" },
  ],
  ...over,
});

describe("ArchitectureCard", () => {
  it("renders the summary and a diagram with 4 labelled boxes coloured by kind", () => {
    renderWithProviders(<ArchitectureCard architecture={arch()} />);

    const diagram = screen.getByRole("img", { name: "Architecture diagram" });
    for (const label of ["client", "server.ts", "middleware", "postgres"]) {
      expect(within(diagram).getByText(label)).toBeInTheDocument();
    }
    expect(diagram.querySelectorAll("g[data-kind]")).toHaveLength(4);
    expect(diagram.querySelector('g[data-kind="store"] rect')).toHaveStyle({ stroke: "var(--ok)" });
    expect(diagram.querySelectorAll("line")).toHaveLength(3);
    expect(screen.getByText("src/server.ts").tagName).toBe("CODE");
  });

  it("hides the diagram area for a single node but keeps the summary", () => {
    renderWithProviders(
      <ArchitectureCard architecture={arch({ nodes: [{ id: "a", label: "only", kind: "entry" }], edges: [] })} />,
    );

    expect(screen.queryByRole("img", { name: "Architecture diagram" })).not.toBeInTheDocument();
    expect(screen.getByText(/Requests enter through/)).toBeInTheDocument();
  });

  it("shows the empty state without summary and diagram", () => {
    renderWithProviders(<ArchitectureCard architecture={arch({ summary: "", nodes: [], edges: [] })} />);
    expect(screen.getByText("Not enough information")).toBeInTheDocument();
  });

  it("shows LLM text as text, not markup", () => {
    renderWithProviders(<ArchitectureCard architecture={arch({ summary: "<img src=x onerror=alert(1)> **bold**" })} />);
    expect(screen.getByText("<img src=x onerror=alert(1)> **bold**")).toBeInTheDocument();
  });
});
