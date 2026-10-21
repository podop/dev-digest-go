import { afterEach, describe, expect, it } from "vitest";
import type { FindingRecord } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { EvalCaseButton } from "./EvalCaseButton";

afterEach(cleanup);

const FINDING: FindingRecord = {
  id: "f1",
  severity: "WARNING",
  category: "security",
  title: "Hardcoded secret",
  file: "src/config.ts",
  start_line: 11,
  end_line: 12,
  rationale: "r",
  suggestion: null,
  confidence: 0.9,
  kind: "finding",
  trifecta_components: null,
  evidence: null,
  review_id: "r1",
  accepted_at: "2026-10-14T10:00:00.000Z",
  dismissed_at: null,
  eval_case_id: null,
};

describe("EvalCaseButton", () => {
  it("creates the case with a spinner, then becomes an 'Eval case ✓' link to the agent's Evals tab", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const api = mockFetch({
      "POST /findings/f1/eval-case": async () => {
        await gate;
        return jsonResponse({ id: "case1", agent_id: "ag1" }, 201);
      },
      "GET /pulls/pr1/reviews": [],
    });
    const { user } = renderWithProviders(<EvalCaseButton finding={FINDING} agentId="ag1" prId="pr1" />);

    await user.click(screen.getByRole("button", { name: "Turn into eval case" }));
    expect(screen.getByRole("button", { name: "Turn into eval case" })).toBeDisabled();
    release();

    const link = await screen.findByRole("link", { name: /Eval case/ });
    expect(link).toHaveAttribute("href", "/agents/ag1?tab=evals");
    expect(api.requests("POST", "/findings/f1/eval-case")).toHaveLength(1);
  });

  it("is disabled with the reason for an undecided finding or a review without an agent", () => {
    const undecided = { ...FINDING, accepted_at: null };
    const { rerender } = renderWithProviders(<EvalCaseButton finding={undecided} agentId="ag1" prId="pr1" />);
    expect(screen.getByRole("button", { name: "Turn into eval case" })).toBeDisabled();
    expect(screen.getByTitle("Accept or dismiss first")).toBeInTheDocument();

    rerender(<EvalCaseButton finding={FINDING} agentId={null} prId="pr1" />);
    expect(screen.getByRole("button", { name: "Turn into eval case" })).toBeDisabled();
    expect(screen.getByTitle("No agent for this review")).toBeInTheDocument();
  });

  it("shows a 422 inline and re-enables the button", async () => {
    mockFetch({
      "POST /findings/f1/eval-case": jsonResponse({ error: { code: "patch_unavailable", message: "no patch" } }, 422),
    });
    const { user } = renderWithProviders(<EvalCaseButton finding={FINDING} agentId="ag1" prId="pr1" />);

    await user.click(screen.getByRole("button", { name: "Turn into eval case" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("No diff is stored for this file");
    await waitFor(() => expect(screen.getByRole("button", { name: "Turn into eval case" })).toBeEnabled());
  });

  it("shows the link at once when the finding already carries a case id", () => {
    renderWithProviders(<EvalCaseButton finding={{ ...FINDING, eval_case_id: "case1" }} agentId="ag1" prId="pr1" />);
    expect(screen.getByRole("link", { name: /Eval case/ })).toHaveAttribute("href", "/agents/ag1?tab=evals");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("takes every visible string from the message files (pseudo bundle)", () => {
    renderWithProviders(<EvalCaseButton finding={{ ...FINDING, accepted_at: null }} agentId="ag1" prId="pr1" />, {
      messages: pseudoMessages(),
    });
    expect(screen.getByRole("button")).toHaveTextContent("⟦prReview.finding.evalCase⟧");
    expect(screen.getByTitle("⟦prReview.finding.evalCaseUndecided⟧")).toBeInTheDocument();
  });
});
