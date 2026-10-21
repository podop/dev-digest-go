import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import type { RunTrace } from "@devdigest/shared";

const TRACE: RunTrace = {
  config: { agent: "Security", version: "1", provider: "openai", model: "gpt-4.1", pr: 482, source: "local" },
  stats: { duration_ms: 8200, tokens_in: 12000, tokens_out: 1500, cost_usd: 0.06, findings: 2, grounding: "2/2 passed" },
  prompt_assembly: { system: "You are a reviewer.", skills: "### skill", memory: null, specs: null, user: "Review PR #482" },
  tool_calls: [{ tool: "review_file", args: "src/config.ts", meta: "single-pass", ms: 1200 }],
  raw_output: '{"verdict":"request_changes"}',
  memory_pulled: [{ pr: 471, text: "rate-limit public endpoints" }],
  specs_read: [],
  log: [
    { t: "00.10", kind: "info", msg: "Starting review with agent Security" },
    { t: "00.90", kind: "result", msg: "Citation grounding: 2/2 passed" },
  ],
};

import { RunTraceDrawer } from "./RunTraceDrawer";

afterEach(cleanup);

/** A finished run's drawer: the persisted trace comes from GET /runs/r1/trace. */
function renderDrawer(trace: RunTrace = TRACE) {
  const api = mockFetch({ "GET /runs/r1/trace": trace });
  const view = renderWithProviders(
    <div data-theme="dark">
      <RunTraceDrawer runId="r1" agentName="Security" prNumber={482} onClose={() => {}} />
    </div>,
  );
  return { ...view, api };
}

describe("A5 Run Trace drawer (smoke)", () => {
  it("renders the trace tabs and stats", async () => {
    const { api } = renderDrawer();
    expect(await screen.findByText("Configuration")).toBeInTheDocument();
    expect(api.requests("GET", "/runs/r1/trace")).toHaveLength(1);
    expect(screen.getByText("Stats")).toBeInTheDocument();
    expect(screen.getByText("2/2 passed")).toBeInTheDocument();
    expect(screen.getByText("Tool calls")).toBeInTheDocument();
  });

  it("shows the run cost between TOKENS and FINDINGS", async () => {
    renderDrawer();
    expect(await screen.findByText("COST")).toBeInTheDocument();
    expect(screen.getByText("$0.06")).toBeInTheDocument();
  });

  it("an old trace without cost_usd shows a dash", async () => {
    const { cost_usd: _drop, ...oldStats } = TRACE.stats;
    renderDrawer({ ...TRACE, stats: oldStats });
    expect(await screen.findByText("COST")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("a failed run shows the usage it spent before the error", async () => {
    renderDrawer({
      ...TRACE,
      stats: { duration_ms: 900, tokens_in: 4200, tokens_out: 10, cost_usd: 0.011, findings: 0, grounding: "0/0 passed" },
    });
    expect(await screen.findByText("4k→0.0k")).toBeInTheDocument();
    expect(screen.getByText("$0.011")).toBeInTheDocument();
  });

  it("lists the skills that were in the prompt, with their versions", async () => {
    renderDrawer({ ...TRACE, skills_used: [{ id: "sk1", name: "secret-leakage-gate", version: 3 }] });
    expect(await screen.findByText("secret-leakage-gate v3")).toBeInTheDocument();
  });

  it("an old trace without skills_used has no Skills row", async () => {
    renderDrawer();
    expect(await screen.findByText("Configuration")).toBeInTheDocument();
    expect(screen.queryByText("Skills")).toBeNull();
  });

  it("weighs each prompt block on its own: the skills block carries its own token count", async () => {
    const { user } = renderDrawer();
    await user.click(await screen.findByRole("button", { name: /Prompt assembly/ }));
    // "### skill" = 9 chars → 3 tokens; "You are a reviewer." = 19 chars → 5.
    expect(screen.getByRole("button", { name: "Skills (dynamic)" })).toBeInTheDocument();
    expect(screen.getByText("~3 tokens")).toBeInTheDocument();
    expect(screen.getByText("~5 tokens")).toBeInTheDocument();
  });

  it("a run whose skills were all disabled has NO skills block at all", async () => {
    const { user } = renderDrawer({ ...TRACE, prompt_assembly: { ...TRACE.prompt_assembly, skills: null } });
    await user.click(await screen.findByRole("button", { name: /Prompt assembly/ }));
    expect(screen.queryByRole("button", { name: "Skills (dynamic)" })).toBeNull();
    expect(screen.queryByText("~3 tokens")).toBeNull();
    expect(screen.getByRole("button", { name: "System" })).toBeInTheDocument();
  });

  it("lists project-context documents with status; included ones open to the exact text sent", async () => {
    const { user } = renderDrawer({
      ...TRACE,
      project_context: {
        budget_tokens: 16000,
        tokens_total: 1200,
        docs: [
          { path: "docs/architecture.md", doc_type: "docs", origin: { kind: "agent" }, tokens: 800, status: "included", text: "api must not import <db> directly" },
          { path: "specs/auth.md", doc_type: "specs", origin: { kind: "skill", skill_id: "sk1", skill_name: "secret-gate" }, tokens: 400, status: "included", text: "Tokens expire in 1h" },
          { path: "docs/gone.md", doc_type: "docs", origin: { kind: "agent" }, tokens: 0, status: "missing" },
        ],
      },
    });
    // Configuration → Specs read: included paths with their tokens, a skipped one with its status.
    const specsRead = (await screen.findByText("Specs read")).parentElement as HTMLElement;
    expect(within(specsRead).getByText(/docs\/architecture\.md/)).toHaveTextContent("docs/architecture.md · ~800 tok");
    expect(within(specsRead).getByText(/specs\/auth\.md/)).toHaveTextContent("specs/auth.md · ~400 tok");
    expect(within(specsRead).getByText(/docs\/gone\.md/)).toHaveTextContent("docs/gone.md missing");

    await user.click(screen.getByRole("button", { name: /Prompt assembly/ }));
    expect(screen.getByText("Project context · attached specs")).toBeInTheDocument();
    expect(screen.getByText("skill secret-gate")).toBeInTheDocument();
    expect(screen.getAllByText("missing")).toHaveLength(2); // Specs read + the block
    // A missing document has nothing to open.
    expect(screen.queryByRole("button", { name: "docs/gone.md" })).toBeNull();

    const arch = screen.getByRole("button", { name: "docs/architecture.md" });
    expect(arch).toHaveAttribute("aria-expanded", "false");
    await user.click(arch);
    expect(arch).toHaveAttribute("aria-expanded", "true");
    // Untrusted text is shown verbatim as text, never as markup.
    expect(screen.getByText("api must not import <db> directly")).toBeInTheDocument();
    expect(screen.queryByText("Tokens expire in 1h")).toBeNull();

    // Copy puts the exact included text on the clipboard; a missing document has no copy action.
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await user.click(screen.getByRole("button", { name: "Copy specs/auth.md" }));
    expect(writeText).toHaveBeenCalledWith("Tokens expire in 1h");
    expect(screen.queryByRole("button", { name: "Copy docs/gone.md" })).toBeNull();
  });

  it("an old trace lists its specs_read paths and has no project-context block", async () => {
    const { user } = renderDrawer({ ...TRACE, specs_read: ["specs/old.md"] });
    expect(await screen.findByText("specs/old.md")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Prompt assembly/ }));
    expect(screen.queryByText("Project context · attached specs")).toBeNull();
  });

  it("switches to the live log tab", async () => {
    const { user } = renderDrawer();
    await user.click(screen.getByText("log"));
    // LiveLogStream renders its filter input
    expect(screen.getByPlaceholderText("Filter log…")).toBeInTheDocument();
  });
});

describe("Run Trace drawer — accessible sections", () => {
  it("section headers, prompt blocks and tool calls are keyboard toggles", async () => {
    const { user } = renderDrawer();
    const config = await screen.findByRole("button", { name: /Configuration/ });
    expect(config).toHaveAttribute("aria-expanded", "true");
    config.focus();
    await user.keyboard("{Enter}");
    expect(config).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Memory pulled")).toBeNull();

    await user.click(screen.getByRole("button", { name: /Prompt assembly/ }));
    const system = screen.getByRole("button", { name: "System" });
    expect(system).toHaveAttribute("aria-expanded", "false");
    system.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByText("You are a reviewer.")).toBeInTheDocument();

    const tool = screen.getByRole("button", { name: /review_file/ });
    await user.click(tool);
    expect(tool).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/single-pass \(preview truncated\)/)).toBeInTheDocument();
  });
});

