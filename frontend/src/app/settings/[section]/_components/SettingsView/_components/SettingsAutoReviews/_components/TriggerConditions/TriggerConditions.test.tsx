import { describe, it, expect, vi, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { TriggerConditions } from "./TriggerConditions";

afterEach(cleanup);

const HINT = "New commits are reviewed only on PRs that were automatically reviewed before.";
const newPr = () => screen.getByRole("checkbox", { name: "On new PR" });
const newCommits = () => screen.getByRole("checkbox", { name: "On new commits to existing PR" });

describe("TriggerConditions", () => {
  it("shows the stored flags and the D6 hint, and requests one flag per click (FR16, AC19)", async () => {
    const onChange = vi.fn();
    const { user, rerender } = renderWithProviders(
      <TriggerConditions value={{ on_new_pr: true, on_new_commits: false }} onChange={onChange} />,
    );

    expect(screen.getByText("Trigger conditions")).toBeInTheDocument();
    expect(screen.getByText(HINT)).toBeInTheDocument();
    expect(newPr()).toBeChecked();
    expect(newCommits()).not.toBeChecked();

    await user.click(newCommits());
    expect(onChange).toHaveBeenLastCalledWith({ on_new_commits: true });
    await user.click(newPr());
    expect(onChange).toHaveBeenLastCalledWith({ on_new_pr: false });

    // Controlled: the boxes keep the stored value until the section feeds back the saved one.
    expect(newPr()).toBeChecked();
    expect(newCommits()).not.toBeChecked();

    rerender(<TriggerConditions value={{ on_new_pr: false, on_new_commits: true }} onChange={onChange} />);
    expect(newPr()).not.toBeChecked();
    expect(newCommits()).toBeChecked();
  });

  it("disables both boxes while a save is in flight (FR20)", async () => {
    const onChange = vi.fn();
    const { user } = renderWithProviders(
      <TriggerConditions value={{ on_new_pr: true, on_new_commits: true }} disabled onChange={onChange} />,
    );

    expect(newPr()).toBeDisabled();
    expect(newCommits()).toBeDisabled();
    await user.click(newPr());
    await user.click(screen.getByText("On new commits to existing PR"));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", () => {
    const { container } = renderWithProviders(
      <TriggerConditions value={{ on_new_pr: true, on_new_commits: true }} onChange={vi.fn()} />,
      { messages: pseudoMessages() },
    );

    expect(screen.getByText(/autoReviewControls\.triggers\.hint/)).toBeInTheDocument();
    expect(screen.getByText(/autoReviewControls\.triggers\.onNewCommits/)).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });
});
