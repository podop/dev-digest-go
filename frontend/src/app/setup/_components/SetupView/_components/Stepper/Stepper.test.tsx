import { afterEach, describe, expect, it } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { Stepper } from "./Stepper";

afterEach(cleanup);

describe("Stepper", () => {
  it("marks done steps with a text equivalent, the current one with aria-current, the rest as plain", () => {
    renderWithProviders(<Stepper current="github" done={{ model: true, github: false, repo: false }} />);
    const items = within(screen.getByRole("list", { name: "Setup steps" })).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("Model provider");
    expect(within(items[0]!).getByText("completed")).toBeInTheDocument();
    expect(items[1]).toHaveTextContent("2GitHub token");
    expect(within(items[1]!).queryByText("completed")).toBeNull();
    expect(items[2]).toHaveTextContent("3Repository");
    expect(items.map((li) => li.getAttribute("aria-current"))).toEqual([null, "step", null]);
  });

  it("shows the current step by its number even when its prerequisite state is done", () => {
    renderWithProviders(<Stepper current="repo" done={{ model: true, github: true, repo: true }} />);
    const items = screen.getAllByRole("listitem");
    expect(items[2]).toHaveAttribute("aria-current", "step");
    expect(items[2]).toHaveTextContent("3Repository");
  });
});
