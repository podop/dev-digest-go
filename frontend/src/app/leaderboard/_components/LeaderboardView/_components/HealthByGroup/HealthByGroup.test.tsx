import { describe, expect, it } from "vitest";
import type { LeaderboardAuthorHealth, LeaderboardRepoHealth } from "@devdigest/shared";
import { renderWithProviders, screen, within } from "@/test/render";
import { repoLookup } from "../../helpers";
import { HealthByAuthor, HealthByRepo } from "./HealthByGroup";

const authors: LeaderboardAuthorHealth[] = [
  { author: "quinn", prs: 2, avg_health: 41.6, avg_findings: 6.25 },
  { author: "wren", prs: 5, avg_health: 72, avg_findings: 2 },
  { author: "zuri", prs: 1, avg_health: 96, avg_findings: 0 },
  { author: "yael", prs: 1, avg_health: null, avg_findings: 1 },
];

describe("HealthByAuthor", () => {
  it("keeps the API order and shows a coloured bar, health and summary per author; no health shows a dash", () => {
    renderWithProviders(<HealthByAuthor items={authors} />);

    expect(screen.getByRole("heading", { name: "Health by author" })).toBeInTheDocument();
    const rows = within(screen.getByRole("list", { name: "Health by author" })).getAllByRole("listitem");
    expect(rows.map((r) => r.getAttribute("data-series"))).toEqual(["quinn", "wren", "zuri", "yael"]);

    const quinn = within(rows[0]!);
    expect(quinn.getByText("quinn")).toBeInTheDocument();
    expect(quinn.getByText("42")).toBeInTheDocument();
    expect(quinn.getByText("2 PRs · 6.3 findings")).toBeInTheDocument();
    expect(quinn.getByRole("img", { name: "quinn: average health 42" }).firstElementChild).toHaveStyle({
      width: "41.6%",
      background: "var(--crit)",
    });
    expect(within(rows[1]!).getByRole("img").firstElementChild).toHaveStyle({ background: "var(--warn)" });
    expect(within(rows[2]!).getByRole("img").firstElementChild).toHaveStyle({ background: "var(--ok)" });

    const yael = within(rows[3]!);
    expect(yael.getByText("—")).toBeInTheDocument();
    expect(yael.queryByRole("img")).not.toBeInTheDocument();
    expect(yael.getByText("1 PR · 1 findings")).toBeInTheDocument();
  });

  it("shows the top 8 until Show all N, and Show top 8 collapses again", async () => {
    const many = Array.from({ length: 11 }, (_, i): LeaderboardAuthorHealth => ({
      author: `dev${i}`,
      prs: 1,
      avg_health: 50 + i,
      avg_findings: 1,
    }));
    const { user } = renderWithProviders(<HealthByAuthor items={many} />);

    const list = () => within(screen.getByRole("list", { name: "Health by author" }));
    expect(list().getAllByRole("listitem")).toHaveLength(8);
    await user.click(screen.getByRole("button", { name: "Show all 11" }));
    expect(list().getAllByRole("listitem")).toHaveLength(11);
    await user.click(screen.getByRole("button", { name: "Show top 8" }));
    expect(list().getAllByRole("listitem")).toHaveLength(8);
  });

  it("has no toggle for 8 authors or fewer, and an empty state without any", () => {
    const { unmount } = renderWithProviders(<HealthByAuthor items={authors} />);
    expect(screen.queryByRole("button", { name: /Show/ })).not.toBeInTheDocument();
    unmount();

    renderWithProviders(<HealthByAuthor items={[]} />);
    expect(screen.getByText("No authors")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});

describe("HealthByRepo", () => {
  it("labels GitHub repositories by name and GitLab ones with their host", () => {
    const items: LeaderboardRepoHealth[] = [
      { repo_id: "gl-1", repo: "payments", prs: 3, avg_health: 64, avg_findings: 2.5 },
      { repo_id: "gh-1", repo: "acme/api", prs: 4, avg_health: 90, avg_findings: 0.5 },
    ];
    const repos = repoLookup([
      { id: "gl-1", full_name: "group/payments", provider: "gitlab", host: "gitlab.example.com" },
      { id: "gh-1", full_name: "acme/api" },
    ]);
    renderWithProviders(<HealthByRepo items={items} repos={repos} />);

    const rows = within(screen.getByRole("list", { name: "Health by repository" })).getAllByRole("listitem");
    expect(rows.map((r) => r.getAttribute("data-series"))).toEqual(["gl-1", "gh-1"]);
    expect(within(rows[0]!).getByRole("img", { name: "payments · gitlab.example.com: average health 64" })).toBeInTheDocument();
    expect(within(rows[1]!).getByText("acme/api")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("4 PRs · 0.5 findings")).toBeInTheDocument();
  });

  it("shows the empty state without repositories", () => {
    renderWithProviders(<HealthByRepo items={[]} />);
    expect(screen.getByText("No repositories")).toBeInTheDocument();
  });
});
