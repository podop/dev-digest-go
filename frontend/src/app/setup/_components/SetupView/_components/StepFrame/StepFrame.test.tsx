import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { StepFrame } from "./StepFrame";

afterEach(cleanup);

describe("StepFrame", () => {
  it("renders title, sub and body, and fires Back and the primary action", async () => {
    const onBack = vi.fn();
    const onClick = vi.fn();
    const { user } = renderWithProviders(
      <StepFrame title="T" sub="S" onBack={onBack} primary={{ label: "Go", onClick }}>
        <p>body</p>
      </StepFrame>,
    );
    expect(screen.getByRole("heading", { name: "T" })).toBeInTheDocument();
    expect(screen.getByText("S")).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Back" }));
    await user.click(screen.getByRole("button", { name: "Go" }));
    expect(onBack).toHaveBeenCalledTimes(1);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("has no Back without onBack and keeps a disabled primary action inert", async () => {
    const onClick = vi.fn();
    const { user } = renderWithProviders(
      <StepFrame title="T" sub="S" primary={{ label: "Go", onClick, disabled: true }}>
        <p>body</p>
      </StepFrame>,
    );
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Go" }));
    expect(onClick).not.toHaveBeenCalled();
  });
});
