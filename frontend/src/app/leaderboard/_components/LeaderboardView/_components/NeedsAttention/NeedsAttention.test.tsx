import { describe, expect, it } from "vitest";
import type { LeaderboardNeedsAttention } from "@devdigest/shared";
import { renderWithProviders, screen, within } from "@/test/render";
import { NeedsAttention } from "./NeedsAttention";

const data: LeaderboardNeedsAttention = {
  total: 25,
  items: [
    {
      pr_id: "p1",
      repo_id: "id-p1",
      repo: "acme/api",
      number: 41,
      title: "Rewrite the whole billing pipeline so invoices are generated lazily",
      author: "dana-lee",
      opened_at: "2026-10-17T08:00:00.000Z",
      reasons: [
        { kind: "merged_open_critical", count: 1 },
        { kind: "new_commits" },
      ],
    },
    {
      pr_id: "p2",
      repo_id: "id-p2",
      repo: "acme/web",
      number: 7,
      title: "Fix header",
      author: "sam",
      opened_at: null,
      reasons: [
        { kind: "open_critical", count: 2 },
        { kind: "open_long", days: 6 },
        { kind: "no_review" },
      ],
    },
  ],
};

describe("NeedsAttention", () => {
  it("lists each PR with its author and every reason as text, and notes the cap", () => {
    renderWithProviders(<NeedsAttention data={data} />);

    expect(screen.getByRole("heading", { name: "Needs attention" })).toBeInTheDocument();
    expect(screen.getByText("25")).toBeInTheDocument();
    expect(screen.getByText("Showing 2 of 25")).toBeInTheDocument();

    const items = within(screen.getByRole("list", { name: "Needs attention" })).getAllByRole("listitem");
    expect(items).toHaveLength(2);

    const first = within(items[0]!);
    expect(first.getByText("acme/api#41")).toBeInTheDocument();
    expect(first.getByText(data.items[0]!.title)).toBeInTheDocument();
    expect(first.getByText("dana-lee")).toBeInTheDocument();
    expect(first.getByText("DL")).toBeInTheDocument();
    expect(first.getByText("merged with 1 open critical")).toBeInTheDocument();
    expect(first.getByText("new commits since last review")).toBeInTheDocument();

    const second = within(items[1]!);
    expect(second.getByText("acme/web#7")).toBeInTheDocument();
    expect(second.getByText("2 open critical")).toBeInTheDocument();
    expect(second.getByText("open 6d")).toBeInTheDocument();
    expect(second.getByText("no review yet")).toBeInTheDocument();
  });

  it("shows the empty state and no cap note when nothing needs attention", () => {
    renderWithProviders(<NeedsAttention data={{ total: 0, items: [] }} />);

    expect(screen.getByText("Nothing needs attention")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(screen.queryByText(/Showing/)).not.toBeInTheDocument();
  });
});
