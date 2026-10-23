import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { row, review } from "../../fixtures";
import { RowReviews } from "./RowReviews";

afterEach(cleanup);

describe("RowReviews", () => {
  it("lists each agent's review with score and findings, and links to the PR", () => {
    renderWithProviders(
      <RowReviews
        row={row({
          reviews: [
            review({ review_id: "r1", agent_name: "Security agent", score: 82 }),
            review({ review_id: "r2", agent_name: "Deleted agent", agent_id: null, score: null, findings: { CRITICAL: 0, WARNING: 3, SUGGESTION: 0 } }),
          ],
        })}
      />,
    );
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(within(items[0]!).getByText("Security agent")).toBeInTheDocument();
    expect(within(items[0]!).getByText("Score 82")).toBeInTheDocument();
    expect(within(items[0]!).getByText("C 1")).toBeInTheDocument();
    expect(within(items[1]!).getByText("—")).toBeInTheDocument();
    expect(within(items[1]!).getByText("W 3")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open PR acme/api#12" })).toHaveAttribute("href", "/repos/r1/pulls/12");
  });

  it("says so when the window snapshot has no reviews", () => {
    renderWithProviders(<RowReviews row={row({ reviews: [] })} />);
    expect(screen.getByText("No reviews in this window")).toBeInTheDocument();
    expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
  });
});
