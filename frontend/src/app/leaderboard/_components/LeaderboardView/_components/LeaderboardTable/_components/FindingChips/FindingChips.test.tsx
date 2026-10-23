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

  it("outlines the CRITICAL chip in red only while some critical findings are still open", () => {
    const counts = { CRITICAL: 2, WARNING: 1, SUGGESTION: 0 };
    const { rerender } = renderWithProviders(<FindingChips counts={counts} openCritical={1} />);
    expect(screen.getByRole("img", { name: "2 critical (1 still open), 1 warning, 0 suggestion" })).toBeInTheDocument();
    expect(screen.getByText("C 2").style.borderColor).toBe("var(--crit)");
    expect(screen.getByText("W 1").style.borderColor).toBe("");

    rerender(<FindingChips counts={counts} openCritical={0} />);
    expect(screen.getByRole("img", { name: "2 critical, 1 warning, 0 suggestion" })).toBeInTheDocument();
    expect(screen.getByText("C 2").style.borderColor).toBe("");
  });
});
