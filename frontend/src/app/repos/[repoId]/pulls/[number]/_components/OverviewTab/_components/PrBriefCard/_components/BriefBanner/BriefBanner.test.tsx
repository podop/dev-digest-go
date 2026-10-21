/* BriefBanner — verdict/score/counts of the current reviews over the brief's summary and cost. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { BriefBanner, type BriefBannerProps } from "./BriefBanner";

afterEach(cleanup);

const props: BriefBannerProps = {
  summary: "Adds rate limiting.",
  stats: { hasReviews: true, verdict: "request_changes", score: 80, findings: 4, blockers: 1 },
  tokensIn: 3000,
  tokensOut: 400,
  costUsd: 0.012,
  stale: false,
  onRefresh: vi.fn(),
};

describe("BriefBanner", () => {
  it("shows verdict, counts, score, summary and the brief cost, and refreshes on click", async () => {
    const onRefresh = vi.fn();
    const { user } = renderWithProviders(<BriefBanner {...props} onRefresh={onRefresh} />);

    expect(screen.getByText("Request changes")).toBeInTheDocument();
    expect(screen.getByText(/4 findings · 1 blocker/)).toBeInTheDocument();
    expect(screen.getByText("PR SCORE")).toBeInTheDocument();
    expect(screen.getByText("Adds rate limiting.")).toBeInTheDocument();
    expect(screen.getByText("$0.012")).toBeInTheDocument();
    expect(screen.getByText("3k→0.4k")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(onRefresh).toHaveBeenCalledOnce();
  });

  it("with no review shows only the summary", () => {
    renderWithProviders(
      <BriefBanner
        {...props}
        stats={{ hasReviews: false, verdict: null, score: null, findings: 0, blockers: 0 }}
      />,
    );
    expect(screen.queryByText("Request changes")).toBeNull();
    expect(screen.queryByText(/finding/)).toBeNull();
    expect(screen.queryByText("PR SCORE")).toBeNull();
    expect(screen.getByText("Adds rate limiting.")).toBeInTheDocument();
  });
});
