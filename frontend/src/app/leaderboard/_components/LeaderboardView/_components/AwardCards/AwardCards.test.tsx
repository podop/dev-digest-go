import { describe, it, expect, afterEach } from "vitest";
import type { LeaderboardAward, LeaderboardAwards } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { AwardCards } from "./AwardCards";
import { awardHref } from "./helpers";

afterEach(cleanup);

const card = (name: string) => within(screen.getByRole("group", { name }));
const REPO_IDS: Record<string, string> = { "acme/api": "r-1", "acme/web": "r 2", "acme/gone": "r-3" };
const win = (repo: string, number: number, value: number, title = `PR ${number}`): LeaderboardAward => ({
  pr_id: `pr-${number}`,
  repo_id: REPO_IDS[repo] ?? "r-x",
  repo,
  number,
  title,
  value,
});

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
    renderWithProviders(<AwardCards awards={awards} />);

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
    expect(card("Monster PR").getByRole("link", { name: "acme/gone#11" })).toHaveAttribute("href", "/repos/r-3/pulls/11");

    expect(card("Priciest Review").getByText("—")).toBeInTheDocument();
    expect(card("Priciest Review").queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(5);
  });

  it("formats the cost award as USD and takes every visible string from the message files", () => {
    const { container } = renderWithProviders(
      <AwardCards awards={{ ...awards, priciest: win("acme/api", 12, 2.5, "Big refactor") }} />,
      { messages: pseudoMessages() },
    );
    expect(strayText(container, [/^[\d,.$]+$/, /^acme\/\w+#\d+$/, /^(PR \d+|Big refactor|Tighten auth|Rework .*)$/])).toEqual([]);
    expect(within(screen.getByRole("group", { name: "⟦leaderboardHighlights.awards.priciest.title⟧" })).getByText("$2.50")).toBeInTheDocument();
  });
});

describe("awardHref", () => {
  it("builds the PR detail path from the award's repository id and encodes it", () => {
    expect(awardHref(win("acme/web", 3, 1))).toBe("/repos/r%202/pulls/3");
  });
});
