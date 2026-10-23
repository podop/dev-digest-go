import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { finding, row } from "./fixtures";

const nav = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));

import { LeaderboardTable } from "./LeaderboardTable";

beforeEach(() => {
  nav.push.mockReset();
});
afterEach(cleanup);

const titles = () => screen.getAllByRole("row").slice(1).map((r) => within(r).queryByText(/^PR /)?.textContent ?? "");

/** The sort button of a column (the Health header also holds the info button). */
const sortButton = (column: RegExp) => within(screen.getByRole("columnheader", { name: column })).getAllByRole("button")[0]!;

/** 11 rows ranked as given: pr "PR 1" … "PR 11", health falling, cost rising, time falling (PR 11 is the quickest). */
const eleven = Array.from({ length: 11 }, (_, i) =>
  row({
    pr_id: `p${i + 1}`,
    repo_id: "r1",
    number: i + 1,
    title: `PR ${i + 1}`,
    health: 100 - i,
    cost_usd: (i + 1) / 100,
    time_to_merge_s: (12 - i) * 3600,
    top_findings: i === 0 ? [finding({ title: "SQL injection" }), finding({ finding_id: "f2", severity: "WARNING", title: "Unbounded retry", state: "accepted" })] : [],
  }),
);

describe("LeaderboardTable", () => {
  it("shows the top 10 with Show all N and cycles a column desc → asc → rank, with Reset to rank", async () => {
    const { user } = renderWithProviders(<LeaderboardTable rows={eleven} />);

    expect(screen.getByRole("heading", { name: "Leaderboard · ranked by health · 11 PRs" })).toBeInTheDocument();
    expect(titles()).toHaveLength(10);
    expect(screen.queryByText("PR 11")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reset to rank" })).not.toBeInTheDocument();

    // Cost descending: the 11th PR is the priciest, and sorting happens before the top-10 cut.
    const costTh = screen.getByRole("columnheader", { name: /cost/i });
    expect(costTh).toHaveAttribute("aria-sort", "none");
    await user.click(sortButton(/cost/i));
    expect(costTh).toHaveAttribute("aria-sort", "descending");
    expect(screen.getByRole("columnheader", { name: /^health/i })).toHaveAttribute("aria-sort", "none");
    expect(titles()[0]).toBe("PR 11");
    expect(screen.queryByText("PR 1")).not.toBeInTheDocument();
    await user.click(sortButton(/cost/i));
    expect(costTh).toHaveAttribute("aria-sort", "ascending");
    expect(titles()[0]).toBe("PR 1");
    await user.click(sortButton(/cost/i));
    expect(costTh).toHaveAttribute("aria-sort", "none");
    expect(titles()[0]).toBe("PR 1");

    // Reset to rank drops the sort at once, from either direction.
    await user.click(sortButton(/cost/i));
    expect(titles()[0]).toBe("PR 11");
    await user.click(screen.getByRole("button", { name: "Reset to rank" }));
    expect(costTh).toHaveAttribute("aria-sort", "none");
    expect(titles()[0]).toBe("PR 1");
    expect(screen.queryByRole("button", { name: "Reset to rank" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show all 11" }));
    expect(titles()).toHaveLength(11);
    await user.click(screen.getByRole("button", { name: "Show top 10" }));
    expect(titles()).toHaveLength(10);
  });

  it("starts the Time column ascending (shortest first), then descending, then back to the rank", async () => {
    const { user } = renderWithProviders(<LeaderboardTable rows={eleven} />);
    const timeTh = screen.getByRole("columnheader", { name: /time/i });

    await user.click(sortButton(/time/i));
    expect(timeTh).toHaveAttribute("aria-sort", "ascending");
    expect(titles()[0]).toBe("PR 11");
    await user.click(sortButton(/time/i));
    expect(timeTh).toHaveAttribute("aria-sort", "descending");
    expect(titles()[0]).toBe("PR 1");
    await user.click(sortButton(/time/i));
    expect(timeTh).toHaveAttribute("aria-sort", "none");
  });

  it("opens the PR from a row click, and the chevron only expands the top findings (aria-expanded)", async () => {
    const { user } = renderWithProviders(<LeaderboardTable rows={eleven} />);

    await user.click(screen.getByText("PR 3"));
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls/3");

    nav.push.mockClear();
    const toggle = screen.getByRole("button", { name: "Show findings of acme/api#1" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(nav.push).not.toHaveBeenCalled();
    expect(screen.getByText(/SQL injection/)).toBeInTheDocument();
    expect(screen.getByText(/Unbounded retry/)).toBeInTheDocument();
    expect(screen.getByText("accepted")).toBeInTheDocument();

    // Opening another row closes the first; a row without findings says so.
    await user.click(screen.getByRole("button", { name: "Show findings of acme/api#2" }));
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/SQL injection/)).not.toBeInTheDocument();
    expect(screen.getByText("No notable findings recorded.")).toBeInTheDocument();
  });

  it("keeps the server rank in the # column while sorted, and sorts missing health last", async () => {
    const { user } = renderWithProviders(
      <LeaderboardTable
        rows={[
          row({ pr_id: "a", number: 1, title: "PR 1", health: 80 }),
          row({ pr_id: "b", number: 2, title: "PR 2", health: null }),
          row({ pr_id: "c", number: 3, title: "PR 3", health: 95 }),
        ]}
      />,
    );
    await user.click(sortButton(/^health/i));
    await user.click(sortButton(/^health/i));
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0]!.textContent)).toEqual(["1", "3", "2"]);
    expect(screen.queryByRole("button", { name: /show all/i })).not.toBeInTheDocument();
  });

  it("has the health info button in the Health header, which does not sort", async () => {
    const { user } = renderWithProviders(<LeaderboardTable rows={eleven} />);
    const info = screen.getByRole("button", { name: "How health is scored" });
    expect(within(screen.getByRole("columnheader", { name: /^health/i })).getByRole("button", { name: "How health is scored" })).toBe(info);
    await user.hover(info);
    expect(screen.getByRole("tooltip")).toHaveTextContent("Weights: critical 10 · warning 3 · suggestion 1");
    expect(screen.getByRole("columnheader", { name: /^health/i })).toHaveAttribute("aria-sort", "none");
  });

  it("takes all its copy from the message files", async () => {
    const { container, user } = renderWithProviders(<LeaderboardTable rows={[row({ top_findings: [finding()] })]} />, { messages: pseudoMessages() });
    await user.click(screen.getByRole("button", { name: /row\.toggle/ }));
    const data = [/^acme\/api#12$/, /^ivan\.petrov$/, /^Add rate limiting$/, /^\d+$/, /^[SMLX]+$/, /^\$[\d.]+$/, /^[↑↓▸▾]$/, /^[A-Z]{1,2}$/, /^internal\/billing\/invoice\.go:\d+$/, /^Security agent$/];
    expect(strayText(container, data)).toEqual([]);
  });
});
