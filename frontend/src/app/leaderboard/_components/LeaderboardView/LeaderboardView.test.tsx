/* LeaderboardView — the /leaderboard screen: URL state and the request it makes (AC15), every section of
   a loaded page, the empty / loading / error states (AC18), the per-repository sync note (AC19), the
   30d page without a comparison (D19) and the no-hard-coded-copy check. AppShell is a passthrough; fetch is stubbed and the real hooks run. */
import type React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { Leaderboard, LeaderboardPeriodKey, Repo } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { browserTimeZone } from "@/lib/hooks";
import { finding, row } from "./_components/LeaderboardTable/fixtures";
import { resolvePeriod, resolveRepoParam } from "./helpers";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { LeaderboardView } from "./LeaderboardView";

beforeEach(() => {
  nav.replace.mockReset();
  nav.push.mockReset();
});
afterEach(cleanup);

const API_ID = "3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b";
const WEB_ID = "9a1c2d3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e";
const GONE_ID = "00000000-0000-4000-8000-000000000000";

const repo = (id: string, full_name: string): Repo => ({
  id,
  workspace_id: "w1",
  owner: "acme",
  name: full_name.split("/")[1] ?? full_name,
  full_name,
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
});
const REPOS = [repo(API_ID, "acme/api"), repo(WEB_ID, "acme/web")];

const GL_ID = "5b7d9f11-2c3e-4a5b-8c6d-7e8f9a0b1c2d";
const GL2_ID = "6c8e0a22-3d4f-4b6c-9d7e-8f9a0b1c2d3e";
const glRepo = (id: string, host: string, full_name = "acme/platform/payments"): Repo => ({
  ...repo(id, full_name),
  name: full_name.split("/").pop() ?? full_name,
  provider: "gitlab",
  host,
  web_url: `https://${host}/${full_name}`,
  connection_id: "c1",
});

const KPIS = {
  prs_reviewed: 12,
  merged: 7,
  avg_health: 81,
  time_to_first_review_s: 2880,
  time_to_merge_s: 6840,
  critical_found: 4,
  open_critical: 2,
  review_cost_usd: 1.25,
};

const award = (id: string, repoName: string, number: number, value: number, title: string) => ({
  pr_id: `pr-${number}`,
  repo_id: id,
  repo: repoName,
  number,
  title,
  value,
});

function loaded(over: Partial<Leaderboard> = {}): Leaderboard {
  return {
    period: {
      key: "week",
      tz: "Europe/Kyiv",
      from: "2026-09-28T00:00:00.000+03:00",
      to: "2026-10-05T00:00:00.000+03:00",
      previous_from: "2026-09-21T00:00:00.000+03:00",
      previous_to: "2026-09-28T00:00:00.000+03:00",
    },
    repos: [
      { id: API_ID, full_name: "acme/api", last_polled_at: new Date(Date.now() - 5 * 60_000).toISOString() },
      { id: WEB_ID, full_name: "acme/web", last_polled_at: null },
    ],
    summary: KPIS,
    previous_summary: { ...KPIS, prs_reviewed: 9, merged: 8 },
    awards: {
      cleanest: award(API_ID, "acme/api", 12, 98, "Tighten auth"),
      hottest: award(WEB_ID, "acme/web", 7, 5, "Fix header"),
      fastest_merge: award(API_ID, "acme/api", 13, 5400, "Bump deps"),
      most_rounds: award(API_ID, "acme/api", 14, 4, "Rework billing"),
      monster: award(WEB_ID, "acme/web", 15, 12_400, "Vendor the SDK"),
      priciest: award(API_ID, "acme/api", 16, 2.5, "Big refactor"),
    },
    rows: [
      row({ pr_id: "pr-12", repo_id: API_ID, repo: "acme/api", number: 12, title: "Tighten auth", author: "ivan.petrov", top_findings: [finding({ title: "Missing token check" })] }),
      row({ pr_id: "pr-7", repo_id: WEB_ID, repo: "acme/web", number: 7, title: "Fix header", author: "olga.k", health: 60 }),
    ],
    needs_attention: {
      total: 1,
      items: [
        {
          pr_id: "pr-41",
          repo_id: API_ID,
          repo: "acme/api",
          number: 41,
          title: "Rewrite billing",
          author: "dana-lee",
          opened_at: "2026-10-01T08:00:00.000Z",
          reasons: [{ kind: "open_critical", count: 2 }],
        },
      ],
    },
    hot_files: [{ path: "internal/billing/invoice.go", repo_id: API_ID, repo: "acme/api", findings: 5, critical: 2, prs: [41, 44] }],
    health_by_size: [
      { size: "XS", prs: 1, avg_health: 96, avg_findings: 0 },
      { size: "S", prs: 3, avg_health: 90, avg_findings: 0.5 },
      { size: "M", prs: 2, avg_health: 70, avg_findings: 2 },
      { size: "L", prs: 0, avg_health: null, avg_findings: null },
      { size: "XL", prs: 1, avg_health: 40, avg_findings: 6 },
    ],
    activity: {
      granularity: "day",
      points: [
        { from: "2026-09-28", to: "2026-09-28", opened: 2, merged: 1, critical: 0 },
        { from: "2026-09-29", to: "2026-09-29", opened: 3, merged: 2, critical: 1 },
      ],
    },
    ...over,
  };
}

const EMPTY: Leaderboard = loaded({
  summary: { ...KPIS, prs_reviewed: 0, merged: 0, avg_health: null, time_to_first_review_s: null, time_to_merge_s: null, critical_found: 0, open_critical: 0, review_cost_usd: 0 },
  previous_summary: { ...KPIS, prs_reviewed: 0, merged: 0, avg_health: null, time_to_first_review_s: null, time_to_merge_s: null, critical_found: 0, open_critical: 0, review_cost_usd: 0 },
  awards: { cleanest: null, hottest: null, fastest_merge: null, most_rounds: null, monster: null, priciest: null },
  rows: [],
  needs_attention: { total: 0, items: [] },
  hot_files: [],
  health_by_size: (["XS", "S", "M", "L", "XL"] as const).map((size) => ({ size, prs: 0, avg_health: null, avg_findings: null })),
  activity: { granularity: "day", points: [{ from: "2026-09-28", to: "2026-09-28", opened: 0, merged: 0, critical: 0 }] },
});

/** 30d: no previous window and no previous summary (D19); activity per Monday-started week. */
const THIRTY_DAYS = loaded({
  period: {
    key: "30d",
    tz: "Europe/Kyiv",
    from: "2026-09-05T00:00:00.000+03:00",
    to: "2026-10-05T00:00:00.000+03:00",
    previous_from: null,
    previous_to: null,
  },
  previous_summary: null,
  activity: {
    granularity: "week",
    points: [
      { from: "2026-09-05", to: "2026-09-06", opened: 1, merged: 0, critical: 0 },
      { from: "2026-09-28", to: "2026-10-04", opened: 4, merged: 3, critical: 2 },
    ],
  },
});

const searchOf = (req: { search: string }) => new URLSearchParams(req.search);

function view(period: LeaderboardPeriodKey = "week", repoId = "all") {
  return <LeaderboardView period={period} repo={repoId} />;
}

describe("LeaderboardView", () => {
  it("AC18/AC19: a loaded page shows every section with text values and a sync note per repository", async () => {
    const api = mockFetch({ "GET /repos": REPOS, "GET /leaderboard": loaded() });
    renderWithProviders(view());

    expect(await screen.findByRole("heading", { level: 1, name: "PR Leaderboard" })).toBeInTheDocument();
    expect(await screen.findByText("Week of Sep 28 – Oct 4 · compared with Sep 21 – 27")).toBeInTheDocument();

    // KPI tiles and awards
    expect(await screen.findByText("PRs reviewed")).toBeInTheDocument();
    expect(screen.getByText("Review cost")).toBeInTheDocument();
    expect(screen.getAllByRole("group", { name: /PR$|Merge$|Rounds$|Review$/ })).toHaveLength(6);
    expect(within(screen.getByRole("group", { name: "Cleanest PR" })).getByRole("link", { name: "acme/api#12" })).toHaveAttribute("href", `/repos/${API_ID}/pulls/12`);

    // table beside the side panels
    const table = within(screen.getByRole("table", { name: "Pull requests ranked by health" }));
    expect(table.getByText("Tighten auth")).toBeInTheDocument();
    expect(table.getByText("Fix header")).toBeInTheDocument();
    const attention = within(screen.getByRole("list", { name: "Needs attention" }));
    expect(attention.getByRole("link", { name: "acme/api#41" })).toHaveAttribute("href", `/repos/${API_ID}/pulls/41`);
    expect(attention.getByText("2 open critical")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Hot files" })).getByText("internal/billing/invoice.go")).toBeInTheDocument();

    // bottom row
    const sizes = within(screen.getByRole("list", { name: "Health by PR size" }));
    expect(sizes.getAllByRole("listitem")).toHaveLength(5);
    expect(sizes.getByText("3 PRs · 0.5 findings")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Tue 29: 3 opened, 2 merged, 1 critical" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Daily activity" })).toBeInTheDocument();

    // AC19: when each repository was last polled
    const sync = within(screen.getByRole("list", { name: "Repository sync" }));
    expect(sync.getByText(/synced 5 minutes ago/)).toBeInTheDocument();
    expect(sync.getByText(/never polled/)).toBeInTheDocument();

    expect(api.requests("GET", "/leaderboard")).toHaveLength(1);
  });

  it("AC33: a GitLab repository shows !n and its host in rows, awards, needs attention, hot files and the filter; GitHub stays as is", async () => {
    const gl = glRepo(GL_ID, "gitlab.com");
    const gl2 = glRepo(GL2_ID, "git.corp.example", "acme/web");
    const data = loaded({
      repos: [
        { id: API_ID, full_name: "acme/api", last_polled_at: null },
        { id: WEB_ID, full_name: "acme/web", last_polled_at: null },
        { id: GL_ID, full_name: gl.full_name, last_polled_at: null, provider: "gitlab", host: "gitlab.com", web_url: gl.web_url! },
        { id: GL2_ID, full_name: gl2.full_name, last_polled_at: null, provider: "gitlab", host: "git.corp.example", web_url: gl2.web_url! },
      ],
      awards: {
        ...loaded().awards,
        cleanest: award(GL_ID, "payments", 12, 98, "Tighten auth"),
      },
      rows: [
        row({ pr_id: "pr-g", repo_id: GL_ID, repo: "payments", number: 12, title: "GitLab MR", author: "ivan.petrov" }),
        row({ pr_id: "pr-h", repo_id: API_ID, repo: "acme/api", number: 12, title: "GitHub PR", author: "olga.k" }),
      ],
      needs_attention: {
        total: 1,
        items: [
          { pr_id: "pr-a", repo_id: GL2_ID, repo: "web", number: 3, title: "Stale MR", author: "dana-lee", opened_at: null, reasons: [{ kind: "no_review" }] },
        ],
      },
      hot_files: [
        { path: "a.go", repo_id: GL_ID, repo: "payments", findings: 2, critical: 0, prs: [12, 14] },
        { path: "b.go", repo_id: API_ID, repo: "acme/api", findings: 1, critical: 0, prs: [41] },
      ],
    });
    mockFetch({ "GET /repos": [...REPOS, gl, gl2], "GET /leaderboard": data });
    const { user } = renderWithProviders(view());

    const cleanest = within(await screen.findByRole("group", { name: "Cleanest PR" }));
    expect(cleanest.getByRole("link", { name: "payments!12 · gitlab.com" })).toHaveAttribute("href", `/repos/${GL_ID}/pulls/12`);

    const table = within(screen.getByRole("table", { name: "Pull requests ranked by health" }));
    expect(table.getByRole("link", { name: "payments!12 · gitlab.com" })).toHaveAttribute("href", `/repos/${GL_ID}/pulls/12`);
    expect(table.getByRole("link", { name: "acme/api#12" })).toBeInTheDocument();

    const attention = within(screen.getByRole("list", { name: "Needs attention" }));
    expect(attention.getByRole("link", { name: "web!3 · git.corp.example" })).toHaveAttribute("href", `/repos/${GL2_ID}/pulls/3`);

    const hot = within(screen.getByRole("list", { name: "Hot files" }));
    const glFile = within(hot.getByText("a.go").closest("li")!);
    expect(glFile.getByText(/payments · gitlab\.com/)).toBeInTheDocument();
    expect(glFile.getByRole("link", { name: "!12" })).toHaveAttribute("href", `/repos/${GL_ID}/pulls/12`);
    expect(glFile.getByRole("link", { name: "!14" })).toBeInTheDocument();
    const ghFile = within(hot.getByText("b.go").closest("li")!);
    expect(ghFile.getByRole("link", { name: "#41" })).toBeInTheDocument();

    // the filter names same-named projects on two hosts apart, GitHub as today (FR44)
    const select = screen.getByRole("combobox", { name: "Repository" });
    await pickOption(user, select, "acme/web · git.corp.example");
    expect(nav.replace).toHaveBeenLastCalledWith(`/leaderboard?period=week&repo=${GL2_ID}`);
    await pickOption(user, screen.getByRole("combobox", { name: "Repository" }), "acme/platform/payments · gitlab.com");
    expect(nav.replace).toHaveBeenLastCalledWith(`/leaderboard?period=week&repo=${GL_ID}`);
    await pickOption(user, screen.getByRole("combobox", { name: "Repository" }), "acme/web");
    expect(nav.replace).toHaveBeenLastCalledWith(`/leaderboard?period=week&repo=${WEB_ID}`);
    expect(within(screen.getByRole("list", { name: "Repository sync" })).getByText("acme/platform/payments · gitlab.com")).toBeInTheDocument();
  });

  it("AC15: the request carries period, tz and repo; switching period and repo replaces the URL and refetches", async () => {
    const api = mockFetch({ "GET /repos": REPOS, "GET /leaderboard": loaded() });
    const { user, rerender } = renderWithProviders(view("week", API_ID));

    expect(await screen.findByRole("list", { name: "Needs attention" })).toBeInTheDocument();
    const first = searchOf(api.requests("GET", "/leaderboard")[0]!);
    expect(first.get("period")).toBe("week");
    expect(first.get("repo")).toBe(API_ID);
    expect(first.get("tz")).toBe(browserTimeZone());
    expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("acme/api");

    await user.click(within(screen.getByRole("group", { name: "Period" })).getByRole("button", { name: "30d" }));
    expect(nav.replace).toHaveBeenLastCalledWith(`/leaderboard?period=30d&repo=${API_ID}`);
    await pickOption(user, screen.getByRole("combobox", { name: "Repository" }), "acme/web");
    expect(nav.replace).toHaveBeenLastCalledWith(`/leaderboard?period=week&repo=${WEB_ID}`);
    await pickOption(user, screen.getByRole("combobox", { name: "Repository" }), "All repositories");
    expect(nav.replace).toHaveBeenLastCalledWith("/leaderboard?period=week&repo=all");

    // the page re-renders with the new URL state → a new request, no placeholder data
    rerender(view("30d", WEB_ID));
    await waitFor(() => expect(api.requests("GET", "/leaderboard")).toHaveLength(2));
    const second = searchOf(api.requests("GET", "/leaderboard")[1]!);
    expect(second.get("period")).toBe("30d");
    expect(second.get("repo")).toBe(WEB_ID);
    expect(second.get("tz")).toBe(browserTimeZone());
  });

  it("AC15: garbage URL values and a repository that no longer exists fall back without an error request", async () => {
    const api = mockFetch({ "GET /repos": REPOS, "GET /leaderboard": loaded() });
    // the page normalizes the raw ?period= / ?repo= before it renders the view
    const garbage = renderWithProviders(view(resolvePeriod("yesterday"), resolveRepoParam("' OR 1=1 --")));
    expect(await screen.findByRole("list", { name: "Needs attention" })).toBeInTheDocument();
    garbage.unmount();

    const removed = renderWithProviders(view(resolvePeriod(["30d", "week"]), resolveRepoParam(GONE_ID)));
    expect(await screen.findByRole("list", { name: "Needs attention" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("All repositories");
    removed.unmount();

    const requests = api.requests("GET", "/leaderboard").map(searchOf);
    expect(requests).toHaveLength(2);
    expect(requests[0]!.get("period")).toBe("week");
    expect(requests[0]!.get("repo")).toBe("all");
    expect(requests[1]!.get("period")).toBe("30d");
    expect(requests[1]!.get("repo")).toBe("all");
    expect(screen.queryByText("Could not load the leaderboard")).not.toBeInTheDocument();
  });

  it("AC18: empty data shows the empty state of every section, with a button to the PR list", async () => {
    mockFetch({ "GET /repos": REPOS, "GET /leaderboard": EMPTY });
    const { user } = renderWithProviders(view("week", WEB_ID));

    expect(await screen.findByText("No PRs were reviewed in this period")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText("Nothing needs you this week")).toBeInTheDocument();
    expect(screen.getByText("No hot files")).toBeInTheDocument();
    expect(screen.getByText("No PRs by size")).toBeInTheDocument();
    expect(screen.getByText("No activity")).toBeInTheDocument();
    // no award winner → a dash on each card
    expect(within(screen.getByRole("group", { name: "Cleanest PR" })).getByText("—")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /#\d+$/ })).not.toBeInTheDocument();

    // the button leads to the PR list of the filtered repository
    await user.click(screen.getByRole("button", { name: "Go to Pull Requests" }));
    expect(nav.push).toHaveBeenCalledWith(`/repos/${WEB_ID}/pulls`);
  });

  it("D19: the 30d page has no comparison — no label, no deltas — and a weekly activity chart", async () => {
    mockFetch({ "GET /repos": REPOS, "GET /leaderboard": THIRTY_DAYS });
    renderWithProviders(view("30d"));

    expect(await screen.findByText("Sep 5 – Oct 4")).toBeInTheDocument();
    expect(screen.queryByText(/compared with/)).not.toBeInTheDocument();
    expect(await screen.findByText("PRs reviewed")).toBeInTheDocument();
    expect(screen.queryByText(/↑|↓|no change/)).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Weekly activity" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Wk of Sep 28: 4 opened, 3 merged, 2 critical" })).toBeInTheDocument();
    expect(screen.getByText("Wk of Sep 5")).toBeInTheDocument();
  });

  it("FR21: a row click opens the PR; the chevron expands its top findings", async () => {
    mockFetch({ "GET /repos": REPOS, "GET /leaderboard": loaded() });
    const { user } = renderWithProviders(view());

    const table = within(await screen.findByRole("table", { name: "Pull requests ranked by health" }));
    await user.click(table.getByRole("button", { name: "Show findings of acme/api#12" }));
    expect(table.getByText(/Missing token check/)).toBeInTheDocument();
    expect(nav.push).not.toHaveBeenCalled();

    await user.click(table.getByText("Fix header"));
    expect(nav.push).toHaveBeenCalledWith(`/repos/${WEB_ID}/pulls/7`);
  });

  it("AC18: loading shows no digits; without repositories the page offers to add one and shows no table", async () => {
    let release: (value: Response) => void = () => {};
    mockFetch({
      "GET /repos": REPOS,
      "GET /leaderboard": () => new Promise<Response>((resolve) => (release = resolve)),
    });
    renderWithProviders(view());

    const loading = await screen.findByLabelText("Loading the leaderboard");
    expect(loading.textContent).not.toMatch(/\d/);
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText("PRs reviewed")).not.toBeInTheDocument();
    release(jsonResponse(loaded()));
    expect(await screen.findByText("PRs reviewed")).toBeInTheDocument();
    cleanup();

    mockFetch({ "GET /repos": [], "GET /leaderboard": loaded() });
    renderWithProviders(view());
    expect(await screen.findByText("No repositories yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a repository" })).toHaveAttribute("href", "/onboarding");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("AC18: an error shows Retry that loads the page again", async () => {
    const api = mockFetch({
      "GET /repos": REPOS,
      "GET /leaderboard": jsonResponse({ error: { code: "internal", message: "boom" } }, 500),
    });
    const { user } = renderWithProviders(view());

    expect(await screen.findByText("Could not load the leaderboard")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText("PRs reviewed")).not.toBeInTheDocument();

    api.on("GET /leaderboard", () => jsonResponse(loaded()));
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("table", { name: "Pull requests ranked by health" })).toBeInTheDocument();
  });

  it("AC19: every visible string comes from the message files (rendered with a pseudo bundle)", async () => {
    mockFetch({ "GET /repos": REPOS, "GET /leaderboard": loaded() });
    const { container } = renderWithProviders(view(), { messages: pseudoMessages() });
    await screen.findByRole("table", { name: /leaderboardTable\.tableLabel/ });
    // Data only: repository names, PR numbers and titles, authors, paths, numbers and money.
    const stray = strayText(container, [
      /^acme\/(api|web)(#\d+)?$/,
      /^(Tighten auth|Fix header|Bump deps|Rework billing|Vendor the SDK|Big refactor|Rewrite billing)$/,
      /^(ivan\.petrov|olga\.k|dana-lee)$/,
      "internal/billing/invoice.go",
      /^[\d,.$%+\-–↑↓ ]+$/,
      /^(IP|OK|DL)$/,
      /^[A-Z]$/,
      /^#\d*$/,
      /^[A-Z][a-z]{2} \d+$/,
      "▸",
    ]);
    expect(stray).toEqual([]);
  });
});
