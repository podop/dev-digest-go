import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { RunsTable } from "./RunsTable";
import { toggleSelected } from "./helpers";

afterEach(cleanup);

const RUNS = [
  makeRun({ id: "r3", agent_version: 3, recall: 0.75, precision: 0.8, citation_accuracy: 0.9, cases_passed: 6, cases_errored: 1 }),
  makeRun({ id: "r2", agent_version: 2, recall: null, precision: null, citation_accuracy: null, status: "failed", error: "boom" }),
  makeRun({ id: "r1", agent_version: 1, status: "running", cases_finished: 3, recall: null, precision: null, citation_accuracy: null }),
];

describe("RunsTable", () => {
  it("renders rows with whole percents, pass counts, running progress and failure, no checkboxes without selection", () => {
    renderWithProviders(<RunsTable runs={RUNS} />);
    const [, first, second, third] = screen.getAllByRole("row");
    expect(within(first!).getByText("v3")).toBeInTheDocument();
    expect(within(first!).getByText("75%")).toBeInTheDocument();
    expect(within(first!).getByText(/6 \/ 8 passed/)).toBeInTheDocument();
    expect(within(first!).getByText(/1 errored/)).toBeInTheDocument();
    expect(within(second!).getByText("Failed")).toBeInTheDocument();
    expect(within(third!).getByText("Running 3 / 8")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("selects finished runs by keyboard through labelled checkboxes; a running run is not selectable", async () => {
    const onToggle = vi.fn();
    const { user } = renderWithProviders(<RunsTable runs={RUNS} selection={{ selectedIds: ["r2"], onToggle }} />);

    const boxes = screen.getAllByRole("checkbox");
    expect(boxes).toHaveLength(3);
    for (const box of boxes) expect(box).toHaveAccessibleName(/^Select run v\d · /);
    expect(boxes[1]).toBeChecked();
    expect(boxes[2]).toBeDisabled();

    await user.tab();
    expect(boxes[0]).toHaveFocus();
    await user.keyboard(" ");
    expect(onToggle).toHaveBeenCalledWith("r3");
    await user.tab();
    await user.keyboard(" ");
    expect(onToggle).toHaveBeenLastCalledWith("r2");
  });

  it("shows an Agent column only in the skill variant and renders text literally", () => {
    const hostile = "<img src=x onerror=alert(1)>";
    const { container } = renderWithProviders(
      <RunsTable runs={[{ ...makeRun(), agent_name: hostile, model: hostile }]} variant="skill" />,
    );
    expect(screen.getByRole("columnheader", { name: "Agent" })).toBeInTheDocument();
    expect(screen.getAllByText(hostile)).toHaveLength(2);
    expect(container.querySelector("img")).toBeNull();
  });

  it("labels skill runs with the skill version next to the host agent and model", () => {
    const run = makeRun({ owner_kind: "skill", skill_id: "sk1", skill_version: 5, agent_version: 3, agent_name: "Host Reviewer", model: "mock/host" });
    renderWithProviders(<RunsTable runs={[run]} variant="skill" selection={{ selectedIds: [], onToggle: () => {} }} />);
    const row = screen.getAllByRole("row")[1]!;
    expect(within(row).getByText("v5")).toBeInTheDocument();
    expect(within(row).queryByText("v3")).toBeNull();
    expect(within(row).getByText("Host Reviewer")).toBeInTheDocument();
    expect(within(row).getByText("mock/host")).toBeInTheDocument();
    expect(within(row).getByRole("checkbox")).toHaveAccessibleName(/^Select run v5 · /);
  });

  it("has no Agent column in the default agent variant", () => {
    renderWithProviders(<RunsTable runs={[makeRun({ agent_name: "Reviewer" })]} />);
    expect(screen.queryByRole("columnheader", { name: "Agent" })).toBeNull();
  });

  it("takes every visible string from the message files", () => {
    const { container } = renderWithProviders(
      <RunsTable runs={RUNS.map((r) => ({ ...r, agent_name: "Reviewer" }))} variant="skill" selection={{ selectedIds: [], onToggle: () => {} }} />,
      { messages: pseudoMessages() },
    );
    // data: the date, the model, the agent name, the cost ("—" / "$…")
    expect(strayText(container, [/\d{1,4}[/.\-]\d{1,2}[/.\-]\d{1,4}/, "mock/reviewer", "Reviewer", /^[—$<]/])).toEqual([]);
  });
});

describe("toggleSelected", () => {
  it("adds and removes an id, keeping click order", () => {
    expect(toggleSelected(["a"], "b")).toEqual(["a", "b"]);
    expect(toggleSelected(["a", "b"], "a")).toEqual(["b"]);
  });
});
