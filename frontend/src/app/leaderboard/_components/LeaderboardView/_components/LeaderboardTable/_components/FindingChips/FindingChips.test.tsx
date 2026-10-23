import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { FindingChips } from "./FindingChips";

afterEach(cleanup);

describe("FindingChips", () => {
  it("shows C / W / S counts with one spoken label and mutes zero counts", () => {
    renderWithProviders(<FindingChips counts={{ CRITICAL: 2, WARNING: 0, SUGGESTION: 1 }} />);
    expect(screen.getByRole("img", { name: "2 critical, 0 warning, 1 suggestion" })).toBeInTheDocument();
    expect(screen.getByText("C 2").style.color).toBe("var(--crit)");
    expect(screen.getByText("W 0").style.color).toBe("var(--text-muted)");
    expect(screen.getByText("S 1")).toBeInTheDocument();
  });
});
