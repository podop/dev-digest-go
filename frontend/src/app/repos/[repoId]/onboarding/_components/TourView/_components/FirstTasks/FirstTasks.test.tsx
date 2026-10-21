/* FirstTasks — cards with title, monospace path (truncated, tooltip) and a complexity badge. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { FirstTasks } from "./FirstTasks";

afterEach(cleanup);

describe("FirstTasks", () => {
  it("renders each task with its path and complexity", () => {
    renderWithProviders(
      <FirstTasks
        tasks={[
          { title: "Add a /health probe", path: "src/api/health.ts", complexity: "low" },
          { title: "Document the webhook flow", path: "specs/", complexity: "high" },
        ]}
      />,
    );

    const cards = within(screen.getByRole("list", { name: "First tasks" })).getAllByRole("listitem");
    expect(cards).toHaveLength(2);
    expect(within(cards[0] as HTMLElement).getByText("Low complexity")).toBeInTheDocument();
    expect(within(cards[1] as HTMLElement).getByText("High complexity")).toBeInTheDocument();
    expect(within(cards[1] as HTMLElement).getByText("specs/")).toHaveClass("mono");
  });

  it("truncates a 200-character path with the full path as tooltip, no wrap", () => {
    const long = `${"d/".repeat(98)}f.ts`;
    renderWithProviders(<FirstTasks tasks={[{ title: "t", path: long, complexity: "medium" }]} />);

    expect(screen.getByTitle(long)).toHaveStyle({ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" });
  });

  it("shows the empty state", () => {
    renderWithProviders(<FirstTasks tasks={[]} />);
    expect(screen.getByText("Not enough information")).toBeInTheDocument();
  });
});
