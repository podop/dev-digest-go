import { describe, it, expect, afterEach } from "vitest";
import type { LeaderboardAward, LeaderboardAwards, LeaderboardRepo } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { AwardCards } from "./AwardCards";
import { awardHref } from "./helpers";

afterEach(cleanup);

const card = (name: string) => within(screen.getByRole("group", { name }));
const win = (repo: string, number: number, value: number, title = `PR ${number}`): LeaderboardAward => ({
  pr_id: `pr-${number}`,
  repo_id: `id-${repo}`,
  repo,
  number,
  title,
  value,
});

const repos: LeaderboardRepo[] = [
  { id: "r-1", full_name: "acme/api", last_polled_at: null },
  { id: "r 2", full_name: "acme/web", last_polled_at: null },
];
const longTitle = "Rework the authentication middleware so tokens are verified before any handler runs";
const awards: LeaderboardAwards = {
  cleanest: win("acme/api", 7, 98, "Tighten auth"),
  hottest: win("acme/api", 8, 5, longTitle),
  fastest_merge: win("acme/web", 9, 5400),
  most_rounds: win("acme/web", 10, 1),
  monster: win("acme/gone", 11, 12_400),
  priciest: null,
};

describe("AwardCards", () => {
  it("shows six cards with metric, unit, a PR detail link and the title; a missing winner is a dash", () => {
    renderWithProviders(<AwardCards awards={awards} repos={repos} />);

    expect(screen.getAllByRole("group")).toHaveLength(6);
    expect(card("Cleanest PR").getByText("98")).toBeInTheDocument();
    expect(card("Cleanest PR").getByText("health")).toBeInTheDocument();
    expect(card("Cleanest PR").getByRole("link", { name: "acme/api#7" })).toHaveAttribute("href", "/repos/r-1/pulls/7");
    expect(card("Cleanest PR").getByText("Tighten auth")).toBeInTheDocument();

    expect(card("Hottest PR").getByText("5")).toBeInTheDocument();
    expect(card("Hottest PR").getByText("critical")).toBeInTheDocument();
    expect(card("Hottest PR").getByText(longTitle)).toHaveAttribute("title", longTitle);

    expect(card("Fastest Merge").getByText("1h 30m")).toBeInTheDocument();
    expect(card("Fastest Merge").getByText("to merge")).toBeInTheDocument();
    expect(card("Fastest Merge").getByRole("link", { name: "acme/web#9" })).toHaveAttribute("href", "/repos/r%202/pulls/9");

    expect(card("Most Rounds").getByText("round")).toBeInTheDocument();

    expect(card("Monster PR").getByText("12,400")).toBeInTheDocument();
    expect(card("Monster PR").getByText("lines")).toBeInTheDocument();
    // repository outside the filter's list: the reference stays, without a link
    expect(card("Monster PR").getByText("acme/gone#11")).toBeInTheDocument();
    expect(card("Monster PR").queryByRole("link")).not.toBeInTheDocument();

    expect(card("Priciest Review").getByText("—")).toBeInTheDocument();
    expect(card("Priciest Review").queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(4);
  });

  it("formats the cost award as USD and takes every visible string from the message files", () => {
    const { container } = renderWithProviders(
      <AwardCards awards={{ ...awards, priciest: win("acme/api", 12, 2.5, "Big refactor") }} repos={repos} />,
      { messages: pseudoMessages() },
    );
    expect(strayText(container, [/^[\d,.$]+$/, /^acme\/\w+#\d+$/, /^(PR \d+|Big refactor|Tighten auth|Rework .*)$/])).toEqual([]);
    expect(within(screen.getByRole("group", { name: "⟦leaderboardHighlights.awards.priciest.title⟧" })).getByText("$2.50")).toBeInTheDocument();
  });
});

describe("awardHref", () => {
  it("resolves the repository id by name and encodes it", () => {
    expect(awardHref(repos, "acme/web", 3)).toBe("/repos/r%202/pulls/3");
    expect(awardHref(repos, "acme/none", 3)).toBeNull();
  });
});
