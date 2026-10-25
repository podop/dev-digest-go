import { describe, it, expect, vi, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { IntervalSelect } from "./IntervalSelect";
import { intervalChoices, parseMinutes } from "./helpers";

afterEach(cleanup);

const HINT = "Also sets how often PRs are synced for PR Leaderboard.";
const box = () => screen.getByRole("combobox", { name: "Polling interval" });
const labels = () => screen.getAllByRole("option").map((o) => o.textContent);

describe("IntervalSelect", () => {
  it("lists the seven intervals with the hint and requests the picked one (FR14, AC17)", async () => {
    const onChange = vi.fn();
    const { user } = renderWithProviders(<IntervalSelect value={5} onChange={onChange} />);

    expect(box()).toHaveTextContent("Every 5 minutes");
    expect(screen.getByText(HINT)).toBeInTheDocument();

    await user.click(box());
    expect(labels()).toEqual([
      "Every minute",
      "Every 2 minutes",
      "Every 5 minutes",
      "Every 10 minutes",
      "Every 15 minutes",
      "Every 30 minutes",
      "Every hour",
    ]);
    await user.click(screen.getByRole("option", { name: "Every hour" }));
    expect(onChange).toHaveBeenLastCalledWith(60);

    // The select is controlled: until the section feeds back the saved value it still shows 5.
    expect(box()).toHaveTextContent("Every 5 minutes");

    await pickOption(user, box(), "Every minute");
    expect(onChange).toHaveBeenLastCalledWith(1);
    await pickOption(user, box(), "Every 5 minutes");
    expect(onChange).toHaveBeenCalledTimes(2);
  });

  it("shows a stored value outside the list as an extra option, in numeric order (FR14)", async () => {
    const { user } = renderWithProviders(<IntervalSelect value={7} onChange={vi.fn()} />);

    expect(box()).toHaveTextContent("Every 7 minutes");
    await user.click(box());
    expect(labels()).toEqual([
      "Every minute",
      "Every 2 minutes",
      "Every 5 minutes",
      "Every 7 minutes",
      "Every 10 minutes",
      "Every 15 minutes",
      "Every 30 minutes",
      "Every hour",
    ]);
    expect(screen.getByRole("option", { name: "Every 7 minutes" })).toHaveAttribute("aria-selected", "true");
  });

  it("is disabled while a save is in flight (FR20)", () => {
    renderWithProviders(<IntervalSelect value={5} disabled onChange={vi.fn()} />);

    expect(box()).toHaveAttribute("aria-disabled", "true");
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    const { container, user } = renderWithProviders(<IntervalSelect value={7} onChange={vi.fn()} />, {
      messages: pseudoMessages(),
    });

    expect(screen.getByText(/autoReviewControls\.interval\.hint/)).toBeInTheDocument();
    await user.click(screen.getByRole("combobox"));
    expect(screen.getAllByRole("option")).toHaveLength(8);
    expect(strayText(container)).toEqual([]);
  });
});

describe("helpers", () => {
  it("intervalChoices adds only a valid positive integer outside the list", () => {
    expect(intervalChoices(5)).toEqual([1, 2, 5, 10, 15, 30, 60]);
    expect(intervalChoices(90)).toEqual([1, 2, 5, 10, 15, 30, 60, 90]);
    for (const bad of [0, -3, 2.5, Number.NaN]) expect(intervalChoices(bad)).toEqual([1, 2, 5, 10, 15, 30, 60]);
  });

  it("parseMinutes accepts positive integers only", () => {
    expect(parseMinutes("15")).toBe(15);
    for (const bad of ["", "0", "-1", "1.5", "abc"]) expect(parseMinutes(bad)).toBeUndefined();
  });
});
