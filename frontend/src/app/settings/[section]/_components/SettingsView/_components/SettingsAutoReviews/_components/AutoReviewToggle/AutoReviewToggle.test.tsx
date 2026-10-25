import { describe, it, expect, vi, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { AutoReviewToggle } from "./AutoReviewToggle";

afterEach(cleanup);

const ON = "On for every repository in this workspace";
const OFF = "Off — start reviews from a pull request";
const sw = () => screen.getByRole("switch", { name: "Auto-run review on new PR detection" });

describe("AutoReviewToggle", () => {
  it("shows the on/off sublines and requests the opposite value on click (FR13, AC17)", async () => {
    const onChange = vi.fn();
    const { user, rerender } = renderWithProviders(<AutoReviewToggle value={false} onChange={onChange} />);

    expect(sw()).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText(OFF)).toBeInTheDocument();
    expect(screen.queryByText(ON)).not.toBeInTheDocument();

    await user.click(sw());
    expect(onChange).toHaveBeenLastCalledWith(true);

    // The section feeds the saved value back; a failed save simply keeps the old one (AC19).
    rerender(<AutoReviewToggle value={true} onChange={onChange} />);
    expect(sw()).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText(ON)).toBeInTheDocument();
    expect(screen.queryByText(OFF)).not.toBeInTheDocument();

    await user.click(screen.getByText("Auto-run review on new PR detection"));
    expect(onChange).toHaveBeenLastCalledWith(false);
    expect(onChange).toHaveBeenCalledTimes(2);
  });

  it("is disabled and ignores clicks while a save is in flight (FR20)", async () => {
    const onChange = vi.fn();
    const { user } = renderWithProviders(<AutoReviewToggle value={true} disabled onChange={onChange} />);

    expect(sw()).toBeDisabled();
    await user.click(sw());
    expect(onChange).not.toHaveBeenCalled();
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", () => {
    const { container } = renderWithProviders(<AutoReviewToggle value={true} onChange={vi.fn()} />, {
      messages: pseudoMessages(),
    });

    expect(screen.getByText(/autoReviewControls\.toggle\.title/)).toBeInTheDocument();
    expect(screen.getByText(/autoReviewControls\.toggle\.on/)).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });
});
