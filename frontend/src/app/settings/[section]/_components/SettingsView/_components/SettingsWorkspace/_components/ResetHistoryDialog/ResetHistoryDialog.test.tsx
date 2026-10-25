/* ResetHistoryDialog — typed-confirm reset of the run history (FR19, FR20, NFR5, AC21).
   Rendered alone with a spy for onClose; the hooks run for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { createAppQueryClient } from "@/lib/providers";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ResetHistoryDialog } from "./ResetHistoryDialog";

afterEach(cleanup);

const STATS = { agents: 3, skills: 2, eval_cases: 1, learnings: 24, repos: 1, runs: 120 };
const RESET = {
  deleted: {
    agent_runs: 120,
    run_traces: 120,
    reviews: 80,
    findings: 301,
    multi_agent_runs: 0,
    eval_suite_runs: 0,
    eval_runs: 2,
    ci_runs: 1,
    composed_reviews: 0,
    learnings: 24,
  },
  prs_unmarked: 5,
};

function setup(onClose = vi.fn()) {
  const view = renderWithProviders(<ResetHistoryDialog workspaceName="acme" onClose={onClose} />, {
    queryClient: createAppQueryClient(),
  });
  return { ...view, onClose };
}

describe("ResetHistoryDialog", () => {
  it("lists what is deleted and kept with the counts, labels itself, focuses the input and explains the disabled button", async () => {
    mockFetch({ "GET /workspace/stats": STATS });
    const { user } = setup();

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleName("Reset run history?");

    expect(await within(dialog).findByText("120 runs with their reviews, findings and traces")).toBeInTheDocument();
    expect(within(dialog).getByText("24 learnings")).toBeInTheDocument();
    expect(within(dialog).getByText(/Agents, skills, eval cases/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Export does not include run history/)).toBeInTheDocument();
    expect(within(dialog).getByText("This month's spend total is kept.")).toBeInTheDocument();

    const input = within(dialog).getByRole("textbox", { name: "Type acme to confirm" });
    expect(input).toHaveFocus();

    const confirm = within(dialog).getByRole("button", { name: "Reset run history" });
    expect(confirm).toBeDisabled();
    expect(confirm).toHaveAccessibleDescription(/stays disabled until the text matches the workspace name exactly/);

    await user.type(input, "Acme");
    expect(confirm).toBeDisabled();
    await user.clear(input);
    await user.type(input, "acme");
    expect(confirm).toBeEnabled();
    expect(within(dialog).queryByText(/stays disabled/)).not.toBeInTheDocument();
    await user.type(input, " ");
    expect(confirm).toBeDisabled();
  });

  it("a 200 sends the typed name, toasts the counts, closes and refetches the stats", async () => {
    const api = mockFetch({
      "GET /workspace/stats": STATS,
      "POST /workspace/reset-history": RESET,
    });
    const { user, onClose } = setup();
    await screen.findByText("24 learnings");

    await user.type(screen.getByRole("textbox", { name: "Type acme to confirm" }), "acme");
    await user.click(screen.getByRole("button", { name: "Reset run history" }));

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", "/workspace/reset-history")[0]?.body).toEqual({ confirm: "acme" });
    expect(await screen.findByText("Run history reset: 120 runs, 301 findings and 24 learnings deleted.")).toBeInTheDocument();
    await waitFor(() => expect(api.requests("GET", "/workspace/stats").length).toBeGreaterThan(1));
  });

  it("shows a 409 and a 422 inside the dialog, keeps it open, and a retry can succeed", async () => {
    let answer: Response | unknown = jsonResponse(
      { error: { code: "run_active", message: "2 runs are still running" } },
      409,
    );
    mockFetch({
      "GET /workspace/stats": STATS,
      "POST /workspace/reset-history": () => answer,
    });
    const { user, onClose } = setup();
    await user.type(screen.getByRole("textbox", { name: "Type acme to confirm" }), "acme");
    const confirm = screen.getByRole("button", { name: "Reset run history" });

    await user.click(confirm);
    expect(await screen.findByRole("alert")).toHaveTextContent("2 runs are still running");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(confirm).toBeEnabled();

    answer = jsonResponse(
      { error: { code: "confirmation_mismatch", message: "The confirmation does not match the workspace name" } },
      422,
    );
    await user.click(confirm);
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("The confirmation does not match the workspace name"),
    );
    expect(onClose).not.toHaveBeenCalled();

    answer = RESET;
    await user.click(confirm);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("a 500 toasts the server message and keeps the dialog open without an inline error", async () => {
    mockFetch({
      "GET /workspace/stats": STATS,
      "POST /workspace/reset-history": () => jsonResponse({ error: { code: "internal", message: "Disk busy" } }, 500),
    });
    const { user, onClose } = setup();
    await user.type(screen.getByRole("textbox", { name: "Type acme to confirm" }), "acme");
    await user.click(screen.getByRole("button", { name: "Reset run history" }));

    expect(await within(screen.getByRole("status")).findByText("Disk busy")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("sends one request while it runs and disables the buttons", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = mockFetch({
      "GET /workspace/stats": STATS,
      "POST /workspace/reset-history": async () => {
        await gate;
        return RESET;
      },
    });
    const { user, onClose } = setup();
    await user.type(screen.getByRole("textbox", { name: "Type acme to confirm" }), "acme");

    await user.dblClick(screen.getByRole("button", { name: "Reset run history" }));
    expect(await screen.findByRole("button", { name: "Resetting…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();

    release();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", "/workspace/reset-history")).toHaveLength(1);
  });
});

describe("ResetHistoryDialog messages (NFR6)", () => {
  it("takes every visible string from the message files (pseudo bundle), refusal state included", async () => {
    mockFetch({
      "GET /workspace/stats": STATS,
      "POST /workspace/reset-history": () =>
        jsonResponse({ error: { code: "run_active", message: "2 runs are still running" } }, 409),
    });
    const { user } = renderWithProviders(<ResetHistoryDialog workspaceName="acme" onClose={() => {}} />, {
      queryClient: createAppQueryClient(),
      messages: pseudoMessages(),
    });

    expect(screen.getByText("⟦workspaceDanger.dialog.title⟧")).toBeInTheDocument();
    expect(await screen.findByText("⟦workspaceDanger.dialog.deleted.runs⟧")).toBeInTheDocument();
    expect(strayText(document.body)).toEqual([]);

    await user.type(screen.getByRole("textbox"), "acme");
    await user.click(screen.getByRole("button", { name: "⟦workspaceDanger.dialog.confirm⟧" }));
    // The refusal text is API data, not copy.
    const alert = await screen.findByRole("alert");
    expect(strayText(document.body, [alert.textContent?.trim() ?? ""])).toEqual([]);
  });
});
