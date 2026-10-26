/* PullsView on GitLab (spec FR42, FR43, FR49; AC32, AC42): `!n` references, the host-specific empty copy and the
   not-connected banner that disables the refresh action but keeps the stored PRs listed. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import type { PrMeta, Repo } from "@/lib/types";

const replace = vi.fn();
const push = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push }),
  usePathname: () => "/repos/r1/pulls",
}));

import { RepoProvider } from "@/lib/repo-context";
import { PullsView } from "./PullsView";

afterEach(() => {
  cleanup();
  replace.mockReset();
  push.mockReset();
});

const GITLAB: Repo = {
  id: "r1",
  owner: "acme/platform",
  name: "payments",
  full_name: "acme/platform/payments",
  provider: "gitlab",
  host: "git.corp.example",
  web_url: "https://git.corp.example/acme/platform/payments",
  connection_id: "c1",
} as Repo;
const GITHUB = { id: "r1", owner: "acme", name: "api", full_name: "acme/api" } as Repo;

function pr(o: Partial<PrMeta>): PrMeta {
  return {
    number: 1,
    title: "PR",
    author: "a",
    branch: "b",
    base: "main",
    head_sha: "x",
    additions: 1,
    deletions: 1,
    files_count: 1,
    status: "needs_review",
    updated_at: "2026-06-01T00:00:00Z",
    score: null,
    ...o,
  };
}
const PULLS = [pr({ number: 12, title: "Add rate limiting" }), pr({ number: 13, title: "Fix login" })];

function setup(repo: Repo, pulls: PrMeta[] = PULLS, status = "needs_review", messages?: ReturnType<typeof pseudoMessages>) {
  const api = mockFetch({
    "GET /repos": [repo],
    "GET /repos/r1/pulls": pulls,
    "POST /repos/r1/refresh": repo,
  });
  const view = renderWithProviders(
    <RepoProvider>
      <PullsView repoId="r1" status={status} sort="newest" />
    </RepoProvider>,
    { messages },
  );
  return { ...view, api };
}

describe("PullsView — GitLab", () => {
  it("references merge requests as !n, filters by !n and keeps a working refresh when connected", async () => {
    const { user, api } = setup(GITLAB);
    expect(await screen.findByText("Add rate limiting")).toBeInTheDocument();
    expect(screen.getByText("!12")).toBeInTheDocument();
    expect(screen.getByText("!13")).toBeInTheDocument();
    expect(screen.queryByText("#12")).toBeNull();
    expect(screen.queryByRole("note")).toBeNull();

    await user.type(screen.getByPlaceholderText("Filter pull requests…"), "!13");
    expect(screen.queryByText("Add rate limiting")).toBeNull();
    expect(screen.getByText("Fix login")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(api.requests("POST", "/repos/r1/refresh")).toHaveLength(1);
  });

  it("says 'Refresh to sync from GitLab' on an empty list", async () => {
    setup(GITLAB, [], "all");
    expect(await screen.findByText("No open PRs found for this repo yet. Refresh to sync from GitLab.")).toBeInTheDocument();
  });

  it("a not-connected repo shows the banner with the link, disables refresh and still lists the stored PRs (FR49, AC42)", async () => {
    const { user, api } = setup({ ...GITLAB, connection_id: null } as Repo);
    expect(await screen.findByText("Add rate limiting")).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("Not connected — add a GitLab connection for git.corp.example.");
    expect(screen.getByRole("link", { name: "GitLab Integration" })).toHaveAttribute("href", "/settings/gitlab");
    expect(screen.getByText("!12")).toBeInTheDocument();

    const refresh = screen.getByRole("button", { name: "Refresh" });
    expect(refresh).toBeDisabled();
    await user.click(refresh);
    expect(api.requests("POST", "/repos/r1/refresh")).toHaveLength(0);
  });

  it("a not-connected repo with no stored PRs does not promise a sync", async () => {
    setup({ ...GITLAB, connection_id: null } as Repo, [], "all");
    expect(await screen.findByText("No PRs are stored for this repo yet.")).toBeInTheDocument();
    expect(screen.queryByText(/Refresh to sync/)).toBeNull();
  });

  it("takes the banner copy from the message files (pseudo bundle)", async () => {
    setup({ ...GITLAB, connection_id: null } as Repo, PULLS, "needs_review", pseudoMessages());
    expect(await screen.findByRole("link", { name: "⟦prReview.notConnected.link⟧" })).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("⟦prReview.notConnected.body⟧");
    expect(strayText(screen.getByRole("note"))).toEqual([]);
  });
});

describe("PullsView — GitHub is unchanged", () => {
  it("keeps #n, the GitHub empty copy and an enabled refresh", async () => {
    setup(GITHUB, [pr({ number: 12, title: "Add rate limiting" })]);
    expect(await screen.findByText("#12")).toBeInTheDocument();
    expect(screen.queryByText("!12")).toBeNull();
    expect(screen.queryByRole("note")).toBeNull();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
  });

  it("says 'Refresh to sync from GitHub' on an empty list", async () => {
    setup(GITHUB, [], "all");
    expect(await screen.findByText("No open PRs found for this repo yet. Refresh to sync from GitHub.")).toBeInTheDocument();
  });
});
