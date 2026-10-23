import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { row } from "../../fixtures";
import { LeaderboardTableRow } from "./LeaderboardTableRow";

afterEach(cleanup);

function renderRow(o: Parameters<typeof row>[0] = {}, onToggle = () => {}, open = false) {
  return renderWithProviders(
    <table>
      <tbody>
        <LeaderboardTableRow row={row(o)} rank={3} open={open} onToggle={onToggle} />
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

  it("toggles from the chevron button and from a click on the row, not from the PR link", async () => {
    const onToggle = vi.fn();
    const { user } = renderRow({}, onToggle, true);
    const chevron = screen.getByRole("button", { name: "Show reviews of acme/api#12" });
    expect(chevron).toHaveAttribute("aria-expanded", "true");
    await user.click(chevron);
    await user.click(screen.getByText("Add rate limiting"));
    expect(onToggle).toHaveBeenCalledTimes(2);
    const link = screen.getByRole("link", { name: "Open PR acme/api#12" });
    link.addEventListener("click", (e) => e.preventDefault()); // jsdom cannot navigate
    await user.click(link);
    expect(onToggle).toHaveBeenCalledTimes(2);
  });
});
