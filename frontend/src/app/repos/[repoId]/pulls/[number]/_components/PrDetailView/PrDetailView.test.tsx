/* PrDetailView — screen states (loading, error, unknown repo, loaded) and the
   j/k/a/d finding shortcuts, driven through the real TanStack hooks with a
   stubbed fetch. The app chrome (AppShell) is replaced by a passthrough. */
import React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, act } from "@/test/render";
import { mockFetch, jsonResponse, type RouteHandler } from "@/test/fetch-mock";
import { installFakeEventSource } from "@/test/fake-event-source";
import type { FindingRecord, PrDetail, Repo, ReviewRecord } from "@devdigest/shared";

const nav = vi.hoisted(() => ({ search: new URLSearchParams(), replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: nav.replace, push: nav.push }),
  useSearchParams: () => nav.search,
  usePathname: () => "/repos/r1/pulls/7",
}));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { RepoProvider } from "@/lib/repo-context";
import { PrDetailView } from "./PrDetailView";

const REPO = { id: "r1", full_name: "acme/api", owner: "acme", name: "api" } as Repo;
const PR: PrDetail = {
  id: "pr-uuid",
  number: 7,
  title: "Add rate limiting",
  author: "dev",
  branch: "feat/rl",
  base: "main",
  head_sha: "abc123",
  additions: 10,
  deletions: 2,
  files_count: 1,
  status: "open",
  body: "Limits public endpoints.",
  files: [],
  commits: [],
};
const finding = (id: string, title: string): FindingRecord => ({
  id,
  severity: "WARNING",
  category: "security",
  title,
  file: "src/a.ts",
  start_line: 1,
  end_line: 1,
  rationale: "why",
  suggestion: null,
  confidence: 0.9,
  kind: "finding",
  trifecta_components: null,
  evidence: null,
  review_id: "rv1",
  accepted_at: null,
  dismissed_at: null,
});
const REVIEW = {
  id: "rv1",
  pr_id: "pr-uuid",
  agent_id: "a1",
  run_id: "run-1",
  agent_name: "Security Reviewer",
  kind: "review",
  verdict: "comment",
  summary: null,
  score: 70,
  model: "m",
  created_at: "2026-06-13T20:52:51.000Z",
  findings: [finding("f1", "First finding"), finding("f2", "Second finding")],
} as ReviewRecord;

function routes(over: Record<string, RouteHandler> = {}) {
  return mockFetch({
    "GET /repos": [REPO],
    "GET /repos/r1/pulls": [PR],
    "GET /pulls/pr-uuid": PR,
    "GET /pulls/pr-uuid/reviews": [REVIEW],
    "GET /pulls/pr-uuid/runs/active": [],
    "GET /pulls/pr-uuid/runs": [],
    "GET /pulls/pr-uuid/blast": { changed_symbols: [], downstream: [], summary: "0 changed symbols" },
    "GET /agents": [],
    "POST /findings/:id/:action": (req) => ({ finding: { id: req.params.id } }),
    ...over,
  });
}

function renderView() {
  return renderWithProviders(
    <RepoProvider>
      <PrDetailView repoId="r1" number="7" />
    </RepoProvider>,
  );
}

beforeEach(() => {
  nav.search = new URLSearchParams();
  nav.replace.mockClear();
});
afterEach(cleanup);

describe("PrDetailView — states", () => {
  it("shows the skeleton while the PR resolves", () => {
    routes({ "GET /repos/r1/pulls": () => new Promise(() => {}) });
    renderView();
    expect(screen.getByRole("status", { name: "Loading pull request…" })).toBeInTheDocument();
  });

  it("shows the API error and retries the PR request", async () => {
    const api = routes({
      "GET /pulls/pr-uuid": jsonResponse({ error: { code: "boom", message: "Engine exploded" } }, 500),
    });
    const { user } = renderView();
    expect(await screen.findByText("Couldn’t load this pull request")).toBeInTheDocument();
    expect(screen.getByText("Engine exploded")).toBeInTheDocument();
    api.on("GET /pulls/pr-uuid", PR);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: /Add rate limiting/ })).toBeInTheDocument();
  });

  it("an unknown PR number is an error, not an endless skeleton", async () => {
    routes({ "GET /repos/r1/pulls": [] });
    renderView();
    expect(await screen.findByText("PR #7 could not be loaded.")).toBeInTheDocument();
  });

  it("a stale :repoId shows the friendly repo-not-found state", async () => {
    routes({ "GET /repos": [{ ...REPO, id: "other" }] });
    renderView();
    expect(await screen.findByText("No repo selected")).toBeInTheDocument();
    expect(screen.queryByText("Couldn’t load this pull request")).toBeNull();
  });

  it("renders the PR and switches tabs through the URL", async () => {
    routes();
    const { user } = renderView();
    expect(await screen.findByRole("heading", { name: /Add rate limiting/ })).toBeInTheDocument();
    expect(screen.getByText("Limits public endpoints.")).toBeInTheDocument();
    await user.click(screen.getByText("Files changed"));
    expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls/7?tab=diff");
  });

  it("leaving Files changed drops the deep-link focus (?file/?line)", async () => {
    nav.search = new URLSearchParams("tab=diff&file=src%2Fa.ts&line=1&order=original");
    routes();
    const { user } = renderView();
    await user.click(await screen.findByText("Overview"));
    expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls/7?tab=overview&order=original");
  });
});

describe("PrDetailView — finding shortcuts", () => {
  const sent = (api: ReturnType<typeof mockFetch>) =>
    api.requests("POST").map((r) => r.path.replace(/^\/findings\//, ""));

  it("j/k move the focus and a/d act on the focused finding of the newest run", async () => {
    nav.search = new URLSearchParams("tab=findings");
    const api = routes();
    const { user } = renderView();
    expect(await screen.findByText("Second finding")).toBeInTheDocument();

    await user.keyboard("a");
    await waitFor(() => expect(sent(api)).toEqual(["f1/accept"]));
    await user.keyboard("j");
    await user.keyboard("d");
    await waitFor(() => expect(sent(api)).toEqual(["f1/accept", "f2/dismiss"]));
    await user.keyboard("k");
    await user.keyboard("d");
    await waitFor(() => expect(sent(api)).toEqual(["f1/accept", "f2/dismiss", "f1/dismiss"]));
  });

  it("ignores shortcuts with a modifier key", async () => {
    nav.search = new URLSearchParams("tab=findings");
    const api = routes();
    const { user } = renderView();
    expect(await screen.findByText("Second finding")).toBeInTheDocument();
    await user.keyboard("{Control>}a{/Control}");
    await act(() => new Promise((r) => setTimeout(r, 20)));
    expect(sent(api)).toEqual([]);
  });
});

describe("PrDetailView — run trace drawer", () => {
  const ACTIVE = { run_id: "run-9", agent_id: "a1", agent_name: "Security Reviewer", ran_at: null };

  it("streams the live log for an in-flight run", async () => {
    const ES = installFakeEventSource();
    nav.search = new URLSearchParams("trace=run-9");
    routes({ "GET /pulls/pr-uuid/runs/active": [ACTIVE] });
    renderView();
    expect(await screen.findByText("PR #7 · running")).toBeInTheDocument();
    await waitFor(() => expect(ES.all("run-9")).toHaveLength(1));
    act(() =>
      ES.for("run-9").emit("info", { runId: "run-9", seq: 1, kind: "info", msg: "chunk 1 of 3", t: "00.10" }),
    );
    expect(await screen.findByText(/chunk 1 of 3/)).toBeInTheDocument();
  });

  it("does not open a stream for a finished run", async () => {
    const ES = installFakeEventSource();
    nav.search = new URLSearchParams("trace=run-1");
    routes({ "GET /runs/run-1/trace": jsonResponse({ error: { code: "not_found", message: "no" } }, 404) });
    renderView();
    expect(await screen.findByText("PR #7 · completed")).toBeInTheDocument();
    expect(ES.all("run-1")).toHaveLength(0);
  });
});

describe("PrDetailView — GitLab (FR42, FR43, FR49; AC32, AC42)", () => {
  const GITLAB = {
    id: "r1",
    owner: "acme/platform",
    name: "payments",
    full_name: "acme/platform/payments",
    provider: "gitlab",
    host: "git.corp.example",
    web_url: "https://git.corp.example/gitlab/acme/platform/payments",
    connection_id: "c1",
  } as Repo;

  it("shows !n and 'View on GitLab' linking to the merge request under web_url", async () => {
    document.title = "PR #7 · DevDigest";
    routes({ "GET /repos": [GITLAB] });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const { user } = renderView();
    expect(await screen.findByRole("heading", { name: /!7\s*Add rate limiting/ })).toBeInTheDocument();
    expect(screen.queryByText("#7")).toBeNull();
    expect(screen.queryByRole("note")).toBeNull();
    expect(screen.queryByRole("button", { name: "View on GitHub" })).toBeNull();
    await waitFor(() => expect(document.title).toBe("PR !7 · DevDigest"));

    await user.click(screen.getByRole("button", { name: "View on GitLab" }));
    expect(open).toHaveBeenCalledWith(
      "https://git.corp.example/gitlab/acme/platform/payments/-/merge_requests/7",
      "_blank",
      "noopener,noreferrer",
    );
    open.mockRestore();
  });

  it("never links to a non-https web_url", async () => {
    routes({ "GET /repos": [{ ...GITLAB, web_url: "javascript:alert(1)" }] });
    renderView();
    expect(await screen.findByRole("heading", { name: /!7/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View on GitLab" })).toBeDisabled();
  });

  it("an unknown PR number names the merge request as !n", async () => {
    routes({ "GET /repos": [GITLAB], "GET /repos/r1/pulls": [] });
    renderView();
    expect(await screen.findByText("PR !7 could not be loaded.")).toBeInTheDocument();
  });

  it("a not-connected repo shows the banner with the link and still renders the stored PR", async () => {
    routes({ "GET /repos": [{ ...GITLAB, connection_id: null }] });
    renderView();
    expect(await screen.findByRole("heading", { name: /Add rate limiting/ })).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("Not connected — add a GitLab connection for git.corp.example.");
    expect(screen.getByRole("link", { name: "GitLab Integration" })).toHaveAttribute("href", "/settings/gitlab");
    expect(screen.getByText("Limits public endpoints.")).toBeInTheDocument();
  });

  it("GitHub keeps #n, 'View on GitHub' and no banner", async () => {
    routes();
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const { user } = renderView();
    expect(await screen.findByRole("heading", { name: /#7\s*Add rate limiting/ })).toBeInTheDocument();
    expect(screen.queryByRole("note")).toBeNull();
    await user.click(screen.getByRole("button", { name: "View on GitHub" }));
    expect(open).toHaveBeenCalledWith("https://github.com/acme/api/pull/7", "_blank", "noopener,noreferrer");
    open.mockRestore();
  });
});
