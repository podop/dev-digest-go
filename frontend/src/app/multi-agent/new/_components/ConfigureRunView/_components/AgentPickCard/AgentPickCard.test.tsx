import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "@/test/render";
import { AgentPickCard } from "./AgentPickCard";

const agent = { id: "a1", name: "Security Reviewer", description: "Flags secrets.", enabled: true };

describe("AgentPickCard", () => {
  it("is a checkbox named by the agent that toggles on click", async () => {
    const onToggle = vi.fn();
    renderWithProviders(<AgentPickCard agent={agent} on={false} onToggle={onToggle} estimate="8.2s · $0.06" />);
    const box = screen.getByRole("checkbox", { name: /Security Reviewer/ });
    expect(box).toHaveAttribute("aria-checked", "false");
    expect(box).toHaveTextContent("Flags secrets.");
    expect(box).toHaveTextContent("8.2s · $0.06");
    await userEvent.setup().click(box);
    expect(onToggle).toHaveBeenCalledOnce();
  });

  it("draws the name in the primary text colour, not the button's UA colour (prototype dark-theme bug)", () => {
    renderWithProviders(<AgentPickCard agent={agent} on onToggle={() => {}} estimate="—" />);
    expect(screen.getByText("Security Reviewer").style.color).toBe("var(--text-primary)");
    expect(screen.getByRole("checkbox")).toHaveAttribute("aria-checked", "true");
  });

  it("marks a disabled agent", () => {
    renderWithProviders(<AgentPickCard agent={{ ...agent, enabled: false }} on={false} onToggle={() => {}} estimate="—" />);
    expect(screen.getByText("Security Reviewer · disabled")).toBeInTheDocument();
  });
});
