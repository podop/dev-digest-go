/* EvalDashboardView — /eval: agent rows + recent runs + "Run all agents", the empty and
   error states, the per-agent hand-off, and AC34 (no hard-coded copy). AppShell is a
   passthrough; fetch is stubbed and the real hooks run. */
import type React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { EvalOverview } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { AGENT } from "@/test/skill-fixtures";
import { makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { EvalDashboardView } from "./EvalDashboardView";

beforeEach(() => {
  nav.replace.mockReset();
  nav.push.mockReset();
});
afterEach(cleanup);

const LATEST = makeRun({ id: "r2", agent_version: 4, cases_passed: 6, cases_total: 8, recall: 0.75, precision: 0.8, citation_accuracy: 0.9 });

const OVERVIEW: EvalOverview = {
  agents: [
    {
      agent_id: "ag1",
      name: "Security Reviewer",
      model: "gpt-4.1",
      cases_total: 8,
      latest: LATEST,
      sparkline: [
        { run_id: "r1", recall: 0.5, precision: 0.8, citation_accuracy: 0.9 },
        { run_id: "r2", recall: 0.75, precision: 0.8, citation_accuracy: 0.9 },
      ],
    },
    { agent_id: "ag2", name: "Perf Reviewer", model: "claude", cases_total: 3, latest: null, sparkline: [] },
  ],
  recent_runs: [{ ...LATEST, agent_name: "Security Reviewer" }],
};

describe("EvalDashboardView", () => {
  it("lists agent cards and the recent runs of all agents; an agent without a run shows dashes", async () => {
    mockFetch({ "GET /eval/dashboard": OVERVIEW });
    renderWithProviders(<EvalDashboardView agentId={null} period="30d" />);

    expect(await screen.findByRole("heading", { name: "Eval Dashboard" })).toBeInTheDocument();
    expect(screen.getByText("Regression harness across all reviewer agents · pick an agent to see its runs")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "AGENTS" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "RECENT EVAL RUNS · ALL AGENTS" })).toBeInTheDocument();
    const agents = within(await screen.findByRole("list", { name: "Agents and their latest eval run" }));
    expect(agents.getAllByTestId("eval-agent-card")).toHaveLength(2);
    expect(agents.getByRole("link", { name: /Security Reviewer/ })).toHaveAttribute("href", "/eval?agent=ag1");
    expect(agents.getByText("Never run")).toBeInTheDocument();

    const recent = within(screen.getByRole("table", { name: "Recent eval runs of all agents" }));
    expect(recent.getByText("Security Reviewer")).toBeInTheDocument();
    expect(recent.getByText("6/8")).toBeInTheDocument();
  });

  it("Run all agents posts once and reports which agents started and which were skipped", async () => {
    const api = mockFetch({
      "GET /eval/dashboard": OVERVIEW,
      "POST /eval-runs/run-all": () =>
        jsonResponse({ started: [makeRun({ id: "r9", status: "running", finished_at: null })], skipped: [{ agent_id: "ag2", reason: "run_active" }] }, 202),
    });
    const { user } = renderWithProviders(<EvalDashboardView agentId={null} period="30d" />);

    await user.click(await screen.findByRole("button", { name: "Run all agents" }));
    expect(await screen.findByText("1 run started · 1 agent skipped")).toBeInTheDocument();
    expect(api.requests("POST", "/eval-runs/run-all")).toHaveLength(1);
    // The overview is refetched so the agent rows reflect the new run.
    await waitFor(() => expect(api.requests("GET", "/eval/dashboard").length).toBeGreaterThan(1));
  });

  it("shows the empty state with a link to Agents when there is nothing yet", async () => {
    mockFetch({ "GET /eval/dashboard": { agents: [], recent_runs: [] } });
    renderWithProviders(<EvalDashboardView agentId={null} period="30d" />);
    expect(await screen.findByText("No eval runs yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to Agents" })).toHaveAttribute("href", "/agents");
  });

  it("shows an error with Retry that loads the dashboard again", async () => {
    const api = mockFetch({ "GET /eval/dashboard": jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    const { user } = renderWithProviders(<EvalDashboardView agentId={null} period="30d" />);
    expect(await screen.findByText("Could not load the eval dashboard")).toBeInTheDocument();

    api.on("GET /eval/dashboard", OVERVIEW);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("list", { name: "Agents and their latest eval run" })).toBeInTheDocument();
  });

  it("hands ?agent=<id> to the per-agent view", async () => {
    mockFetch({
      "GET /agents": [AGENT],
      "GET /agents/ag1/eval/dashboard": { agent_id: "ag1", cases_total: 0, latest: null, previous: null, trend: [], alert: null },
      "GET /agents/ag1/eval-runs": { runs: [] },
    });
    renderWithProviders(<EvalDashboardView agentId="ag1" period="30d" />);
    expect(await screen.findByRole("heading", { name: "Security Reviewer" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All agents" })).toHaveAttribute("href", "/eval");
  });

  it("AC34: every visible string comes from the message files (rendered with a pseudo bundle)", async () => {
    mockFetch({ "GET /eval/dashboard": OVERVIEW });
    const { container } = renderWithProviders(<EvalDashboardView agentId={null} period="30d" />, { messages: pseudoMessages() });
    await screen.findByRole("list", { name: /eval\.agentsOverview\.caption/ });
    // Data only: agent names, models, local run stamps, cost.
    const stray = strayText(container, ["Security Reviewer", "Perf Reviewer", "gpt-4.1", "claude", "mock/reviewer", /^\$\d/, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/]);
    expect(stray).toEqual([]);
  });
});
