import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { finding, row } from "../../fixtures";
import { LeaderboardTableRow } from "./LeaderboardTableRow";

afterEach(cleanup);

function renderRow(o: Parameters<typeof row>[0] = {}, handlers: { onToggle?: () => void; onOpen?: () => void } = {}, open = false) {
  return renderWithProviders(
    <table>
      <tbody>
        <LeaderboardTableRow row={row(o)} rank={3} open={open} onToggle={handlers.onToggle ?? (() => {})} onOpen={handlers.onOpen ?? (() => {})} />
      </tbody>
    </table>,
  );
}

describe("LeaderboardTableRow", () => {
  it("renders the PR, status, size, risk, health, findings, rounds, times and cost", () => {
    renderRow();
    expect(screen.getByText("acme/api#12")).toBeInTheDocument();
    expect(screen.getByText("ivan.petrov")).toBeInTheDocument();
    expect(screen.getByText("Add rate limiting")).toHaveAttribute("title", "Add rate limiting");
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(screen.getByText("M")).toBeInTheDocument();
    expect(screen.getByText("+120")).toBeInTheDocument();
    expect(screen.getByText("-30")).toBeInTheDocument();
    expect(screen.getByText(/7f/)).toBeInTheDocument();
    expect(screen.getByText("low")).toBeInTheDocument();
    expect(screen.getByText("90")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "0 critical, 2 warning, 0 suggestion" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "acme/api#12" })).toHaveAttribute("href", "/repos/r1/pulls/12");
    expect(screen.getByText("→ review 0.8 h")).toBeInTheDocument();
    expect(screen.getByText("→ merge 1.9 h")).toBeInTheDocument();
    expect(screen.getByText("$0.0039")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("shows the open age of an unmerged PR and dashes for missing health and size", () => {
    renderRow({ status: "open", size: null, health: null, time_to_first_review_s: null, time_to_merge_s: null, open_age_s: 6 * 86400 + 100, merged_at: null });
    expect(screen.getByText("open 6d")).toBeInTheDocument();
    expect(screen.queryByText(/→/)).not.toBeInTheDocument();
    expect(screen.getAllByText("—")).toHaveLength(2);
  });

  it("shows the XS size bucket, and the open critical chip with its outline", () => {
    renderRow({ size: "XS", additions: 20, deletions: 5, files_count: 1, findings: { CRITICAL: 2, WARNING: 0, SUGGESTION: 0 }, open_critical: 1 });
    expect(screen.getByText("XS")).toBeInTheDocument();
    expect(screen.getByText("C 2").style.borderColor).toBe("var(--crit)");
    expect(screen.getByRole("img", { name: "2 critical (1 still open), 0 warning, 0 suggestion" })).toBeInTheDocument();
  });

  it("opens the PR from a click on the row, and only expands from the chevron", async () => {
    const onToggle = vi.fn();
    const onOpen = vi.fn();
    const { user } = renderRow({}, { onToggle, onOpen }, true);

    const chevron = screen.getByRole("button", { name: "Show findings of acme/api#12" });
    expect(chevron).toHaveAttribute("aria-expanded", "true");
    await user.click(chevron);
    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(onOpen).not.toHaveBeenCalled();

    await user.click(screen.getByText("Add rate limiting"));
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onToggle).toHaveBeenCalledTimes(1);

    const link = screen.getByRole("link", { name: "acme/api#12" });
    link.addEventListener("click", (e) => e.preventDefault()); // jsdom cannot navigate
    await user.click(link);
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("shows the top findings of an expanded row, or the empty text", () => {
    const { unmount } = renderRow({ top_findings: [finding({ title: "SQL injection" })] }, {}, true);
    expect(screen.getByText("internal/billing/invoice.go:88")).toBeInTheDocument();
    unmount();
    renderRow({ top_findings: [] }, {}, true);
    expect(screen.getByText("No notable findings recorded.")).toBeInTheDocument();
  });
});
