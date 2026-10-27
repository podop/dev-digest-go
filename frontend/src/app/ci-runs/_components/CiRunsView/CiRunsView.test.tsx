/* CiRunsView — GitHub and GitLab rows (host-aware PR/MR link, job link, host label), a GitLab row of a host that is
   not connected (no MR link), the GitLab refresh error copy, and no hard-coded text (pseudo messages). Real hooks
   over a stubbed API; AppShell is a passthrough. */
import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import type { CiRefreshResult, CiRun } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";

vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { CiRunsView } from "./CiRunsView";

const MR_URL = "https://git.corp.example/gitlab/acme/platform/payments/-/merge_requests/4";
const JOB_URL = "https://git.corp.example/gitlab/acme/platform/payments/-/jobs/991";

function run(over: Partial<CiRun>): CiRun {
  return {
    id: "run",
    ci_installation_id: "i",
    pr_number: 7,
    ran_at: "2026-10-27T10:00:00.000Z",
    status: "succeeded",
    findings_count: 2,
    cost_usd: 0.01,
    github_url: null,
    source: "ci",
    agent: "Security Reviewer",
    duration_s: 12,
    verdict: "passed",
    ...over,
  } as CiRun;
}

const GITHUB = run({
  id: "gh",
  repo: "acme/api",
  pr_number: 7,
  provider: "github",
  host: "github.com",
  github_url: "https://github.com/acme/api/actions/runs/55",
  pr_url: null,
});
const GITLAB = run({
  id: "gl",
  repo: "acme/platform/payments",
  pr_number: 4,
  provider: "gitlab",
  host: "git.corp.example",
  github_url: JOB_URL,
  pr_url: MR_URL,
});
const GITLAB_UNCONNECTED = run({
  id: "gl2",
  repo: "acme/other/svc",
  pr_number: 9,
  provider: "gitlab",
  host: "gitlab.example.org",
  github_url: "https://gitlab.example.org/acme/other/svc/-/jobs/12",
  pr_url: null,
});

const REFRESH: CiRefreshResult = {
  ingested: 0,
  degraded: true,
  installations: [
    { installation_id: "i1", repo: "acme/api", host: "github.com", state: "error", error_code: "github_rate_limited", ingested: 0 },
    { installation_id: "i2", repo: "acme/platform/payments", host: "git.corp.example", state: "error", error_code: "gitlab_rate_limited", ingested: 0 },
    { installation_id: "i3", repo: "acme/other/svc", host: "gitlab.example.org", state: "error", error_code: "repo_host_not_connected", ingested: 0 },
    { installation_id: "i4", repo: "acme/x/y", host: "git.corp.example", state: "error", error_code: "gitlab_token_missing", ingested: 0 },
  ],
} as CiRefreshResult;

afterEach(() => cleanup());

describe("CiRunsView", () => {
  it("shows GitHub rows as before and GitLab rows with !<n>, MR link, job link and host label", async () => {
    mockFetch({ "GET /ci/runs": { runs: [GITHUB, GITLAB, GITLAB_UNCONNECTED] } });
    renderWithProviders(<CiRunsView />);

    const rows = await screen.findAllByRole("row");
    const gh = within(rows[1]!);
    expect(gh.getByText("acme/api")).toBeInTheDocument();
    expect(gh.getByRole("link", { name: "#7" })).toHaveAttribute("href", "https://github.com/acme/api/pull/7");
    expect(gh.getByRole("link", { name: "View" })).toHaveAttribute("href", "https://github.com/acme/api/actions/runs/55");
    expect(gh.queryByText(/GitLab/)).not.toBeInTheDocument();

    const gl = within(rows[2]!);
    expect(gl.getByText("acme/platform/payments · git.corp.example")).toBeInTheDocument();
    expect(gl.getByRole("link", { name: "!4" })).toHaveAttribute("href", MR_URL);
    expect(gl.getByRole("link", { name: "View on GitLab" })).toHaveAttribute("href", JOB_URL);

    // EC34: host not connected — pr_url null: no MR link, `!<n>`, host label and the job link stay.
    const gl2 = within(rows[3]!);
    expect(gl2.getByText("acme/other/svc · gitlab.example.org")).toBeInTheDocument();
    expect(gl2.getByText("!9")).toBeInTheDocument();
    expect(gl2.queryByRole("link", { name: "!9" })).not.toBeInTheDocument();
    expect(gl2.getByRole("link", { name: "View on GitLab" })).toHaveAttribute(
      "href",
      "https://gitlab.example.org/acme/other/svc/-/jobs/12",
    );
  });

  it("renders no link for a non-http(s) job or MR URL", async () => {
    const bad = { ...GITLAB, pr_url: "javascript:alert(1)", github_url: "data:text/html,x" } as CiRun;
    mockFetch({ "GET /ci/runs": { runs: [bad] } });
    renderWithProviders(<CiRunsView />);

    const row = within((await screen.findAllByRole("row"))[1]!);
    expect(row.queryByRole("link")).not.toBeInTheDocument();
    expect(row.getByText("!4")).toBeInTheDocument();
  });

  it("explains the GitLab refresh errors and keeps the raw code of a GitHub one", async () => {
    mockFetch({ "GET /ci/runs": { runs: [GITHUB] }, "POST /ci/refresh": REFRESH });
    const { user } = renderWithProviders(<CiRunsView />);

    await screen.findAllByRole("row");
    await user.click(screen.getByRole("button", { name: "Refresh" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(within(alert).getAllByRole("listitem")).toHaveLength(4));
    const [gh, rate, notConnected, noToken] = within(alert).getAllByRole("listitem");
    expect(gh).toHaveTextContent("acme/api — github_rate_limited");
    expect(rate).toHaveTextContent("acme/platform/payments · git.corp.example — GitLab rate limit reached.");
    expect(notConnected).toHaveTextContent("acme/other/svc · gitlab.example.org — No GitLab connection for this host.");
    expect(noToken).toHaveTextContent("acme/x/y · git.corp.example — The connection for this host has no token.");
  });

  it("takes all of its copy from the message files", async () => {
    mockFetch({ "GET /ci/runs": { runs: [GITHUB, GITLAB, GITLAB_UNCONNECTED] }, "POST /ci/refresh": REFRESH });
    const { container, user } = renderWithProviders(<CiRunsView />, { messages: pseudoMessages() });

    await screen.findAllByRole("row");
    await user.click(screen.getByRole("button"));
    await screen.findByRole("alert");

    const data = [
      /^[#!]\d+$/,
      "—",
      "acme/api",
      "Security Reviewer",
      /^acme\/[a-z/]+ · [a-z.]+$/,
      "github_rate_limited",
      /^[\d/,.: APM-]+$/,
      /^\$?[\d.]+$/,
      /^\d+(\.\d)?s$/,
      /^\d+$/,
    ];
    expect(strayText(container, data)).toEqual([]);
  });
});
