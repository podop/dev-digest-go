import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, userEvent } from "@/test/render";
import { pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { row } from "./fixtures";
import { LeaderboardTable } from "./LeaderboardTable";

afterEach(cleanup);

const titles = () => screen.getAllByRole("row").slice(1).map((r) => within(r).queryByText(/^PR /)?.textContent ?? "");

/** 11 rows ranked as given: pr "PR 1" … "PR 11", health falling, cost rising. */
const eleven = Array.from({ length: 11 }, (_, i) =>
  row({
    pr_id: `p${i + 1}`,
    number: i + 1,
    title: `PR ${i + 1}`,
    health: 100 - i,
    cost_usd: (i + 1) / 100,
    reviews: i === 0 ? [row().reviews[0]!] : [],
  }),
);

describe("LeaderboardTable", () => {
  it("shows the top 10 with Show all N, sorts a column with aria-sort, and expands a row", async () => {
    const user = userEvent.setup();
    renderWithProviders(<LeaderboardTable rows={eleven} />);

    expect(screen.getByRole("heading", { name: "Leaderboard · ranked by health · 11 PRs" })).toBeInTheDocument();
    expect(titles()).toHaveLength(10);
    expect(screen.queryByText("PR 11")).not.toBeInTheDocument();

    // Cost descending: the 11th PR is the priciest, and sorting happens before the top-10 cut.
    const costTh = screen.getByRole("columnheader", { name: /cost/i });
    expect(costTh).toHaveAttribute("aria-sort", "none");
    await user.click(within(costTh).getByRole("button"));
    expect(costTh).toHaveAttribute("aria-sort", "descending");
    expect(screen.getByRole("columnheader", { name: /health/i })).toHaveAttribute("aria-sort", "none");
    expect(titles()[0]).toBe("PR 11");
    expect(screen.queryByText("PR 1")).not.toBeInTheDocument();
    await user.click(within(costTh).getByRole("button"));
    expect(costTh).toHaveAttribute("aria-sort", "ascending");
    expect(titles()[0]).toBe("PR 1");
    await user.click(within(costTh).getByRole("button"));
    expect(costTh).toHaveAttribute("aria-sort", "none");

    await user.click(screen.getByRole("button", { name: "Show all 11" }));
    expect(titles()).toHaveLength(11);
    await user.click(screen.getByRole("button", { name: "Show top 10" }));
    expect(titles()).toHaveLength(10);

    // Expanding shows the reviews of the snapshot; opening another row closes the first.
    const toggle = screen.getByRole("button", { name: "Show reviews of acme/api#1" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Security agent")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show reviews of acme/api#2" }));
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("No reviews in this window")).toBeInTheDocument();
  });

  it("keeps the server rank in the # column while sorted, and sorts missing health last", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <LeaderboardTable
        rows={[
          row({ pr_id: "a", number: 1, title: "PR 1", health: 80 }),
          row({ pr_id: "b", number: 2, title: "PR 2", health: null }),
          row({ pr_id: "c", number: 3, title: "PR 3", health: 95 }),
        ]}
      />,
    );
    await user.click(within(screen.getByRole("columnheader", { name: /health/i })).getByRole("button"));
    await user.click(within(screen.getByRole("columnheader", { name: /health/i })).getByRole("button"));
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0]!.textContent)).toEqual(["1", "3", "2"]);
    expect(screen.queryByRole("button", { name: /show all/i })).not.toBeInTheDocument();
  });

  it("takes all its copy from the message files", () => {
    const { container } = renderWithProviders(<LeaderboardTable rows={[row()]} />, { messages: pseudoMessages() });
    const data = [/^acme\/api#12$/, /^ivan\.petrov$/, /^Add rate limiting$/, /^\d+$/, /^[SMLX]+$/, /^\$[\d.]+$/, /^[↑↓▸▾]$/, /^[A-Z]{1,2}$/];
    expect(strayText(container, data)).toEqual([]);
  });
});
