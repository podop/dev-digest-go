/* RunSteps — AC18: copy writes only the command and confirms for 2 s; a rejecting or missing
   clipboard raises a "Copy failed" toast; AC22: a 300-char command wraps (pre-wrap). */
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { RunSteps } from "./RunSteps";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const STEPS = [
  { command: "pnpm install" },
  { command: "cp .env.example .env", comment: "add OPENAI keys" },
];

/** Call after render: user-event installs its own clipboard stub when the test user is created. */
function stubClipboard(writeText: (t: string) => Promise<void>) {
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
}

describe("RunSteps", () => {
  it("copies only the command and confirms for 2 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const writeText = vi.fn().mockResolvedValue(undefined);
    const { user } = renderWithProviders(<RunSteps steps={STEPS} />);
    stubClipboard(writeText);

    await user.click(screen.getByRole("button", { name: "Copy command 2" }));

    expect(writeText).toHaveBeenCalledWith("cp .env.example .env");
    expect(await screen.findByText("Copied")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(2000);
    });
    expect(screen.queryByText("Copied")).not.toBeInTheDocument();
  });

  it("shows the number, command and comment of each step, with a review-before-running note", () => {
    renderWithProviders(<RunSteps steps={STEPS} />);

    expect(
      screen.getByText("Generated from the repo's files — review each command before running it."),
    ).toBeInTheDocument();

    const second = screen.getAllByRole("listitem")[1] as HTMLElement;
    expect(within(second).getByText("2")).toBeInTheDocument();
    expect(within(second).getByText("cp .env.example .env")).toBeInTheDocument();
    expect(within(second).getByText("# add OPENAI keys")).toBeInTheDocument();
  });

  it("toasts 'Copy failed' when the clipboard rejects", async () => {
    const { user } = renderWithProviders(<RunSteps steps={STEPS} />);
    stubClipboard(() => Promise.reject(new Error("denied")));

    await user.click(screen.getByRole("button", { name: "Copy command 1" }));

    expect(await screen.findByText("Copy failed")).toBeInTheDocument();
    expect(screen.queryByText("Copied")).not.toBeInTheDocument();
  });

  it("toasts 'Copy failed' when the clipboard is unavailable", async () => {
    const { user } = renderWithProviders(<RunSteps steps={STEPS} />);
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });

    await user.click(screen.getByRole("button", { name: "Copy command 1" }));

    expect(await screen.findByText("Copy failed")).toBeInTheDocument();
  });

  it("wraps a 300-character command instead of widening the page", () => {
    const long = "a".repeat(300);
    renderWithProviders(<RunSteps steps={[{ command: long }]} />);

    expect(screen.getByText(long)).toHaveStyle({ whiteSpace: "pre-wrap", overflowWrap: "anywhere" });
  });

  it("shows the empty state", () => {
    renderWithProviders(<RunSteps steps={[]} />);
    expect(screen.getByText("No run steps found in this repo")).toBeInTheDocument();
    expect(screen.queryByText(/review each command/)).not.toBeInTheDocument();
  });
});
