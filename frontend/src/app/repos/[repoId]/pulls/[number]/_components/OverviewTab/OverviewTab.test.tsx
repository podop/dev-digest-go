/* OverviewTab — "PR Brief" banner first, then Intent (with the Risk areas inside) and Blast
   radius as siblings in one grid, then the full-width Review focus card, the description last. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { PrBrief, PrBriefResponse } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, within } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { OverviewTab } from "./OverviewTab";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));

afterEach(cleanup);

const BRIEF: PrBrief = {
  summary: "Adds rate limiting.",
  risks: {
    risks: [{ kind: "security", title: "Limiter bypass", explanation: "Spoofable.", severity: "high", file_refs: ["src/a.ts:12"] }],
  },
  review_focus: [{ file: "src/a.ts", line: 12, reason: "Start here." }],
  intent: null,
  blast: null,
  missing_inputs: [],
  specs_used: [],
  head_sha: "abc",
  generated_at: "2026-02-01T12:00:00Z",
  prompt_version: "v1",
  provider: "openai",
  model: "gpt-4.1",
  tokens_in: 3000,
  tokens_out: 400,
  cost_usd: 0.01,
  model_requests: 1,
};
const WITH_BRIEF: PrBriefResponse = { brief: BRIEF, stale: false };

const ROUTES = {
  "GET /pulls/pr-1/brief": { brief: null, stale: false },
  "GET /pulls/pr-1/reviews": [],
  "GET /pulls/pr-1/runs/active": [],
  "GET /pulls/pr-1/intent": { intent: null, stale: false },
  "GET /pulls/pr-1/blast": { changed_symbols: [], downstream: [], summary: "s" },
  "GET /pulls/pr-1/history": { history: [] },
};

const renderOverview = () =>
  renderWithProviders(
    <OverviewTab
      prId="pr-1"
      repoId="r1"
      number="7"
      changedPaths={["src/a.ts"]}
      prBody="Body text"
      repoFullName="acme/api"
      headSha="abc"
    />,
  );

describe("OverviewTab", () => {
  it("renders the PR Brief first, then Intent and Blast radius as siblings in one grid, then the description", async () => {
    mockFetch(ROUTES);
    renderOverview();

    const brief = await screen.findByRole("region", { name: "PR Brief" });
    const blast = await screen.findByRole("region", { name: "Blast radius" });
    const intentEmpty = await screen.findByRole("button", { name: /derive/i });
    const grid = blast.parentElement!;
    expect(grid.contains(intentEmpty)).toBe(true);
    expect(brief.nextElementSibling).toBe(grid);
    expect(grid.nextElementSibling).toContainElement(screen.getByText("Body text"));
    // No brief yet: no risk areas, no review focus.
    expect(screen.queryByRole("heading", { name: "Risk areas" })).toBeNull();
    expect(screen.queryByRole("heading", { name: /Review focus/ })).toBeNull();
  });

  it("with a brief: risk areas sit inside the Intent card (also before an intent exists), review focus is its own card between the grid and the description", async () => {
    mockFetch({ ...ROUTES, "GET /pulls/pr-1/brief": WITH_BRIEF });
    renderOverview();

    const risks = await screen.findByRole("region", { name: "Risk areas" });
    const blast = await screen.findByRole("region", { name: "Blast radius" });
    const grid = blast.parentElement!;
    expect(grid.contains(risks)).toBe(true);
    expect(risks.contains(blast)).toBe(false);
    expect(within(risks).getByText("Limiter bypass")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /derive/i })).toBeInTheDocument();

    const focus = screen.getByRole("region", { name: /Review focus/ });
    expect(grid.contains(focus)).toBe(false);
    expect(grid.nextElementSibling).toBe(focus);
    expect(focus.nextElementSibling).toContainElement(screen.getByText("Body text"));
  });

  it("generates the brief with one POST however many cards show its state", async () => {
    let release!: (r: PrBriefResponse) => void;
    const pending = new Promise<PrBriefResponse>((res) => {
      release = res;
    });
    const api = mockFetch({ ...ROUTES, "POST /pulls/pr-1/brief": () => pending });
    const { user } = renderOverview();

    await user.click(await screen.findByRole("button", { name: "Generate brief" }));
    await waitFor(() => expect(screen.getByRole("region", { name: "PR Brief" })).toHaveAttribute("aria-busy", "true"));
    expect(screen.queryByRole("heading", { name: "Risk areas" })).toBeNull();
    expect(api.requests("POST", "/pulls/pr-1/brief")).toHaveLength(1);

    release(WITH_BRIEF);
    expect(await screen.findByText("Limiter bypass")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Review focus/ })).toBeInTheDocument();
    expect(api.requests("POST", "/pulls/pr-1/brief")).toHaveLength(1);
  });

  it("watches the PR's active runs for live updates and never generates a brief on its own", async () => {
    const api = mockFetch(ROUTES);
    renderOverview();

    await screen.findByRole("region", { name: "PR Brief" });
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/runs/active").length).toBeGreaterThan(0));
    expect(api.requests("POST")).toHaveLength(0);
  });
});
