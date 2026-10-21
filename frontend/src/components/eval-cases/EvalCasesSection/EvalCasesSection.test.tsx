/* EvalCasesSection — header with the run slot and "New eval case", list states, the limit,
   create through the editor, and owner routing. fetch is stubbed; the real hooks run. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, within } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { makeCase } from "@/test/eval-fixtures";
import { EvalCasesSection } from "./EvalCasesSection";

afterEach(cleanup);

const AGENT = { kind: "agent", id: "ag1" } as const;
const DIFF = ["diff --git a/src/a.ts b/src/a.ts", "--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,1 +1,3 @@", " x", "+y", "+z", ""].join("\n");

describe("EvalCasesSection", () => {
  it("creates a manual case from 'New eval case' and lists it with the manual tag; the run slot sits in the header", async () => {
    let cases = [makeCase({ id: "c1", name: "from-finding" })];
    const api = mockFetch({
      "GET /agents/ag1/eval-cases": () => ({ cases }),
      "POST /agents/ag1/eval-cases": () => {
        const created = makeCase({ id: "c2", name: "hand-made", origin: "manual", source_finding_id: null, source_pr_id: null });
        cases = [...cases, created];
        return jsonResponse(created, 201);
      },
    });
    const { user } = renderWithProviders(<EvalCasesSection owner={AGENT} runSlot={<button>Run slot</button>} emptyText="Nothing yet" />);
    expect(await screen.findByText("0 / 1 passing")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run slot" })).toBeInTheDocument();
    expect(screen.queryByText("manual")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "New eval case" }));
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "hand-made");
    await user.click(within(dialog).getByLabelText("Pasted unified diff"));
    await user.paste(DIFF);
    await user.type(within(dialog).getByLabelText("Start line"), "1");
    await user.type(within(dialog).getByLabelText("End line"), "2");
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Save" })).toBeEnabled());
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("button", { name: "hand-made" })).toBeInTheDocument();
    expect(api.requests("POST", "/agents/ag1/eval-cases")).toHaveLength(1);
    expect(screen.getByText("manual")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  }, 20_000);

  it("disables 'New eval case' with a reason at 200 cases", async () => {
    mockFetch({ "GET /agents/ag1/eval-cases": { cases: Array.from({ length: 200 }, (_, i) => makeCase({ id: `c${i}`, name: `case-${i}` })) } });
    renderWithProviders(<EvalCasesSection owner={AGENT} emptyText="Nothing yet" />);
    await screen.findByText("0 / 200 passing");
    expect(screen.getByRole("button", { name: "New eval case" })).toBeDisabled();
    expect(screen.getByTitle("Limit of 200 eval cases reached — delete one to add another")).toBeInTheDocument();
  });

  it("reads a skill's cases from the skill route and shows the empty text, or an error with Retry", async () => {
    mockFetch({ "GET /skills/sk1/eval-cases": { cases: [] } });
    renderWithProviders(<EvalCasesSection owner={{ kind: "skill", id: "sk1" }} emptyText="Nothing yet" />);
    expect(await screen.findByText("Nothing yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New eval case" })).toBeEnabled();
    cleanup();

    mockFetch({ "GET /skills/sk1/eval-cases": jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    renderWithProviders(<EvalCasesSection owner={{ kind: "skill", id: "sk1" }} emptyText="Nothing yet" />);
    expect(await screen.findByText("Could not load the eval cases")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New eval case" })).toBeDisabled();
  });
});
