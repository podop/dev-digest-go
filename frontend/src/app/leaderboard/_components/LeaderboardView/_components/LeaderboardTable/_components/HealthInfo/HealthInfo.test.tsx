import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { HealthInfo } from "./HealthInfo";

afterEach(cleanup);

describe("HealthInfo", () => {
  it("explains the health formula on hover and on focus, and closes on leave, blur and Escape", async () => {
    const { user } = renderWithProviders(<HealthInfo />);
    const button = screen.getByRole("button", { name: "How health is scored" });
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

    await user.hover(button);
    const tip = screen.getByRole("tooltip");
    expect(tip).toHaveTextContent("100 − Σ weight − 3 × (rounds − 1)");
    expect(tip).toHaveTextContent("Weights: critical 10 · warning 3 · suggestion 1. Floored at 0. Dismissed findings don't count");
    expect(button).toHaveAccessibleDescription(/Weights: critical 10/);
    await user.unhover(button);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

    await user.tab();
    expect(button).toHaveFocus();
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });
});
