/* CiTab — GitLab installations and runs (GitLab C O1): "GitLab CI" + host instead of "GitHub Actions", `!<n>` linked
   to the server's pr_url, "View on GitLab" for the job; GitHub rows unchanged. Hooks run for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { Agent, CiInstallation, CiRun } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { CiTab } from "./CiTab";

afterEach(cleanup);

const AGENT: Agent = {
  id: "ag1",
  name: "Security Reviewer",
  description: "",
  provider: "openai",
  model: "gpt-4.1",
  system_prompt: "x",
  output_schema: null,
  strategy: "single-pass",
  ci_fail_on: "critical",
  repo_intel: true,
  enabled: true,
  version: 1,
};

const GH_INST: CiInstallation = {
  id: "i1",
  agent_id: "ag1",
  repo: "acme/api",
  target_type: "gha",
  installed_at: "2026-10-27T10:00:00Z",
  manifest_version: 1,
  last_run: null,
  provider: "github",
  host: "github.com",
};
const GL_INST: CiInstallation = {
  ...GH_INST,
  id: "i2",
  repo: "acme/platform/payments",
  target_type: "gitlab_ci",
  provider: "gitlab",
  host: "gitlab.example.com",
};

const BASE_RUN: CiRun = {
  id: "r1",
  ci_installation_id: "i1",
  pr_number: 7,
  ran_at: "2026-10-27T10:30:00Z",
  status: "failed",
  findings_count: 2,
  cost_usd: null,
  github_url: "https://github.com/acme/api/actions/runs/1",
  source: "ci",
  agent_id: "ag1",
  repo: "acme/api",
  verdict: "blocked",
  provider: "github",
  host: "github.com",
  pr_url: null,
};
const GL_RUN: CiRun = {
  ...BASE_RUN,
  id: "r2",
  ci_installation_id: "i2",
  pr_number: 3,
  github_url: "https://gitlab.example.com/acme/platform/payments/-/jobs/55",
  repo: "acme/platform/payments",
  provider: "gitlab",
  host: "gitlab.example.com",
  pr_url: "https://gitlab.example.com/acme/platform/payments/-/merge_requests/3",
};

function stub(installations: CiInstallation[], runs: CiRun[]) {
  mockFetch({
    "GET /agents/:id/ci-installations": { installations },
    "GET /ci/runs": { runs },
    "GET /repos": [],
    "GET /settings/custom-providers": [],
  });
}

describe("CiTab with GitLab", () => {
  it("labels a GitLab installation 'GitLab CI' with its host and keeps GitHub Actions on a GitHub one", async () => {
    stub([GH_INST, GL_INST], []);
    renderWithProviders(<CiTab agent={AGENT} />);

    expect(await screen.findByText("acme/platform/payments · gitlab.example.com")).toBeInTheDocument();
    const [github, gitlab] = screen.getAllByRole("listitem");
    expect(within(github!).getByText("acme/api")).toBeInTheDocument();
    expect(within(github!).getByText("GitHub Actions")).toBeInTheDocument();
    expect(within(github!).queryByText("GitLab CI")).not.toBeInTheDocument();
    expect(within(gitlab!).getByText("GitLab CI")).toBeInTheDocument();
    expect(within(gitlab!).queryByText("GitHub Actions")).not.toBeInTheDocument();
  });

  it("shows a GitLab run as '!<n>' linked to pr_url with 'View on GitLab', and a GitHub run as '#<n>' with 'View'", async () => {
    stub([GH_INST, GL_INST], [BASE_RUN, GL_RUN]);
    renderWithProviders(<CiTab agent={AGENT} />);

    const table = await screen.findByRole("table", { name: "CI Runs" });
    // The runs keep the order sent: BASE_RUN (GitHub) first, GL_RUN (GitLab) second; row 0 is the header.
    const rows = within(table).getAllByRole("row");
    const gh = within(rows[1]!);
    const gl = within(rows[2]!);

    expect(gh.getByRole("link", { name: "#7" })).toHaveAttribute("href", "https://github.com/acme/api/pull/7");
    expect(gh.getByRole("link", { name: "View" })).toHaveAttribute("href", BASE_RUN.github_url);
    expect(gl.getByText("acme/platform/payments · gitlab.example.com")).toBeInTheDocument();
    expect(gl.getByRole("link", { name: "!3" })).toHaveAttribute("href", GL_RUN.pr_url);
    expect(gl.getByRole("link", { name: "View on GitLab" })).toHaveAttribute("href", GL_RUN.github_url);
    expect(gl.queryByText("#3")).not.toBeInTheDocument();
  });

  it("keeps a GitLab run's '!<n>' as text without a link when the server sent no pr_url or a non-http one", async () => {
    stub([GL_INST], [{ ...GL_RUN, id: "r3", pr_url: null }, { ...GL_RUN, id: "r4", pr_number: 4, pr_url: "javascript:alert(1)" }]);
    renderWithProviders(<CiTab agent={AGENT} />);

    expect(await screen.findByText("!3")).toBeInTheDocument();
    expect(screen.getByText("!4")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "!3" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "!4" })).not.toBeInTheDocument();
  });

  it("renders no hard-coded copy for a GitLab installation and run (AC35)", async () => {
    stub([GL_INST], [GL_RUN]);
    const { container } = renderWithProviders(<CiTab agent={AGENT} />, { messages: pseudoMessages() });

    expect(await screen.findByText("!3")).toBeInTheDocument();
    expect(await screen.findByText("⟦ci.ciTab.targetGitlab⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦runs.ciRuns.viewOn⟧")).toBeInTheDocument();
    const allowed = [
      "acme/platform/payments · gitlab.example.com",
      /^[#!]\d+$/,
      /^\d{1,2}\/\d{1,2}\/\d{2,4}/, // the run's local date + time
      /^[\d.,]+$/,
      "v1",
      "—",
    ];
    expect(strayText(container, allowed)).toEqual([]);
  });
});
