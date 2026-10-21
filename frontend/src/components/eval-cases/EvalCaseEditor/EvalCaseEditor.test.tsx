import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { act, fireEvent } from "@testing-library/react";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { makeCase } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { pickOption } from "@/test/select";
import { EvalCaseEditor } from "./EvalCaseEditor";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const OWNER = { kind: "agent", id: "ag1" } as const;
const HUNK = "@@ -10,3 +10,4 @@\n ctx\n+added\n+second\n ctx2\n";
const TWO_FILES = ["diff --git a/src/a.ts b/src/a.ts", "--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,1 +1,2 @@", " x", "+y", "diff --git a/src/b.ts b/src/b.ts", "--- a/src/b.ts", "+++ b/src/b.ts", "@@ -5,1 +5,2 @@", " p", "+q", ""].join("\n");
const HOSTILE = "<img src=x onerror=alert(1)>";
/** Typing into a jsdom form re-renders the editor per key; these flows type a lot. */
const SLOW = 20_000;

const dialog = () => screen.getByRole("dialog", { hidden: false });
const save = () => within(dialog()).getByRole("button", { name: "Save" });

async function paste(user: ReturnType<typeof renderWithProviders>["user"], label: string, text: string) {
  await user.click(within(dialog()).getByLabelText(label));
  await user.paste(text);
}

describe("EvalCaseEditor — new case", () => {
  it("previews a header-less hunk for the typed file with the lines highlighted, and offers a headed diff's files", async () => {
    mockFetch({});
    const { user, container } = renderWithProviders(<EvalCaseEditor owner={OWNER} onClose={() => {}} />);
    expect(screen.getByText("Paste a unified diff to preview it")).toBeInTheDocument();
    expect(within(dialog()).getByLabelText("Name")).toHaveFocus();

    await paste(user, "Pasted unified diff", HUNK);
    expect(await screen.findByText("Enter the file path to preview")).toBeInTheDocument();
    await user.type(within(dialog()).getByRole("textbox", { name: "File" }), "src/a.ts");
    await user.type(within(dialog()).getByLabelText("Start line"), "11");
    await user.type(within(dialog()).getByLabelText("End line"), "12");
    await waitFor(() => expect(container.querySelectorAll("[data-highlighted]")).toHaveLength(2));
    expect(within(dialog()).getByText("src/a.ts")).toBeInTheDocument();

    await user.clear(within(dialog()).getByLabelText("Pasted unified diff"));
    await paste(user, "Pasted unified diff", TWO_FILES);
    const fileSelect = await within(dialog()).findByRole("combobox", { name: "File" });
    await waitFor(() => expect(fileSelect).toHaveTextContent("src/a.ts"));
    await pickOption(user, fileSelect, "src/b.ts");
    expect(fileSelect).toHaveTextContent("src/b.ts");
  }, SLOW);

  it("updates the preview 300 ms after the last edit, not before (AC15)", () => {
    mockFetch({});
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const { container } = renderWithProviders(<EvalCaseEditor owner={OWNER} onClose={() => {}} />);
    const change = (label: string, value: string) => fireEvent.change(within(dialog()).getByLabelText(label), { target: { value } });
    change("Pasted unified diff", HUNK);
    change("Start line", "11");
    change("End line", "12");
    fireEvent.change(within(dialog()).getByRole("textbox", { name: "File" }), { target: { value: "src/a.ts" } });

    act(() => void vi.advanceTimersByTime(299));
    expect(container.querySelectorAll("[data-highlighted]")).toHaveLength(0);
    act(() => void vi.advanceTimersByTime(1));
    expect(container.querySelectorAll("[data-highlighted]")).toHaveLength(2);
  });

  it("shows the inline checks and keeps Save disabled until they hold", async () => {
    mockFetch({});
    const { user } = renderWithProviders(<EvalCaseEditor owner={OWNER} onClose={() => {}} />);
    await user.type(within(dialog()).getByLabelText("Name"), "leak");
    await user.type(within(dialog()).getByRole("textbox", { name: "File" }), "src/a.ts");
    await user.type(within(dialog()).getByLabelText("Start line"), "11");
    await user.type(within(dialog()).getByLabelText("End line"), "12");

    await paste(user, "Pasted unified diff", "just words");
    expect(await within(dialog()).findByText("No hunk found — paste a diff with @@ headers", { selector: "span" })).toBeInTheDocument();
    expect(save()).toBeDisabled();

    await user.clear(within(dialog()).getByLabelText("Pasted unified diff"));
    await paste(user, "Pasted unified diff", HUNK);
    await waitFor(() => expect(save()).toBeEnabled());

    await user.clear(within(dialog()).getByLabelText("Start line"));
    await user.type(within(dialog()).getByLabelText("Start line"), "40");
    await user.clear(within(dialog()).getByLabelText("End line"));
    await user.type(within(dialog()).getByLabelText("End line"), "30");
    expect(await within(dialog()).findByText("End line must be at least the start line")).toBeInTheDocument();
    expect(save()).toBeDisabled();
    await user.clear(within(dialog()).getByLabelText("End line"));
    await user.type(within(dialog()).getByLabelText("End line"), "41");
    expect(await within(dialog()).findByText(/These lines are not in the diff\. Changed and context lines: 10–14/)).toBeInTheDocument();
    expect(save()).toBeDisabled();

    fireEvent.change(within(dialog()).getByLabelText("Pasted unified diff"), { target: { value: "x".repeat(200_001) } });
    expect(within(dialog()).getByText("200 001 / 200 000")).toBeInTheDocument();
    expect(within(dialog()).getByText("The diff is over 200 000 characters")).toBeInTheDocument();
    expect(save()).toBeDisabled();
  }, SLOW);

  it("sends one create request on a double click and shows a server rejection at the line fields", async () => {
    const api = mockFetch({
      // answers after a pause, like a real server: the second click lands while the first request is in flight
      "POST /agents/ag1/eval-cases": async () => {
        await new Promise((r) => setTimeout(r, 150));
        return jsonResponse({ error: { code: "expectation_lines_not_in_diff", message: "no", details: { ranges: [{ start: 10, end: 14 }] } } }, 422);
      },
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<EvalCaseEditor owner={OWNER} onClose={onClose} />);
    await user.type(within(dialog()).getByLabelText("Name"), "leak");
    await user.type(within(dialog()).getByRole("textbox", { name: "File" }), "src/a.ts");
    await user.type(within(dialog()).getByLabelText("Start line"), "11");
    await user.type(within(dialog()).getByLabelText("End line"), "12");
    await paste(user, "Pasted unified diff", HUNK);
    await waitFor(() => expect(save()).toBeEnabled());

    await user.dblClick(save());
    expect(await within(dialog()).findByText(/These lines are not in the diff\. Changed and context lines: 10–14/)).toBeInTheDocument();
    expect(api.requests("POST", "/agents/ag1/eval-cases")).toHaveLength(1);
    expect(api.requests("POST", "/agents/ag1/eval-cases")[0]?.body).toMatchObject({
      name: "leak",
      expectation: { kind: "must_find", file: "src/a.ts", start_line: 11, end_line: 12 },
      input_diff: HUNK,
    });
    expect(within(dialog()).getByLabelText("Name")).toHaveValue("leak");
    expect(onClose).not.toHaveBeenCalled();
  }, SLOW);

  it("creates a skill-owned case through the skill route and closes", async () => {
    const api = mockFetch({ "POST /skills/sk1/eval-cases": jsonResponse(makeCase({ owner_kind: "skill", owner_id: "sk1", agent_id: null }), 201) });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<EvalCaseEditor owner={{ kind: "skill", id: "sk1" }} onClose={onClose} />);
    await user.type(within(dialog()).getByLabelText("Name"), "leak");
    await paste(user, "Pasted unified diff", TWO_FILES);
    await user.type(within(dialog()).getByLabelText("Start line"), "1");
    await user.type(within(dialog()).getByLabelText("End line"), "2");
    await waitFor(() => expect(save()).toBeEnabled());
    await user.click(save());
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", "/skills/sk1/eval-cases")[0]?.body).toMatchObject({ expectation: { file: "src/a.ts" } });
  }, SLOW);
});

describe("EvalCaseEditor — discard and focus", () => {
  function Harness() {
    const [open, setOpen] = React.useState(false);
    return (
      <>
        <button onClick={() => setOpen(true)}>New eval case</button>
        {open && <EvalCaseEditor owner={OWNER} onClose={() => setOpen(false)} />}
      </>
    );
  }

  it("closes an untouched form on Escape and returns focus to the opener", async () => {
    mockFetch({});
    const { user } = renderWithProviders(<Harness />);
    await user.click(screen.getByRole("button", { name: "New eval case" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New eval case" })).toHaveFocus();
  });

  it("asks before discarding an edited form; Escape or Keep editing goes back, Discard closes", async () => {
    mockFetch({});
    const { user } = renderWithProviders(<Harness />);
    await user.click(screen.getByRole("button", { name: "New eval case" }));
    await user.type(screen.getByLabelText("Name"), "draft");

    await user.keyboard("{Escape}");
    expect(screen.getByText("Discard changes?")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByText("Discard changes?")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("draft");

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByText("Discard changes?")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Discard" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not close the editor when Escape only closes an open Select", async () => {
    mockFetch({});
    const { user } = renderWithProviders(<EvalCaseEditor owner={OWNER} onClose={() => {}} />);
    await user.click(within(dialog()).getByRole("combobox", { name: "Severity" }));
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.queryByText("Discard changes?")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});

describe("EvalCaseEditor — edit", () => {
  it("prefills a finding-derived case with everything editable and sends only the changes", async () => {
    const evalCase = makeCase({ content_edited_at: "2026-10-15T08:00:00.000Z", last_result: { run_id: "r1", status: "failed", ran_at: "2026-10-14T10:01:00.000Z" } });
    const api = mockFetch({ "PATCH /eval-cases/case1": { ...evalCase, name: "renamed" } });
    const onClose = vi.fn();
    const onDelete = vi.fn();
    const { user } = renderWithProviders(<EvalCaseEditor owner={OWNER} evalCase={evalCase} onClose={onClose} onDelete={onDelete} />);

    expect(within(dialog()).getByText("From a finding")).toBeInTheDocument();
    expect(within(dialog()).getByText("Edited")).toBeInTheDocument();
    expect(within(dialog()).getByText("acme/shop · PR #7")).toBeInTheDocument();
    expect(within(dialog()).getByText(/Failed ·/)).toBeInTheDocument();
    expect(within(dialog()).getByLabelText("Pasted unified diff")).toHaveValue(evalCase.input_diff);
    expect(within(dialog()).getByLabelText("Name")).toHaveFocus();
    expect(save()).toBeDisabled();

    await pickOption(user, within(dialog()).getByRole("combobox", { name: "Expectation kind" }), "Must not flag");
    await user.type(within(dialog()).getByLabelText("Name"), "!");
    await waitFor(() => expect(save()).toBeEnabled());
    await user.click(save());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(api.requests("PATCH", "/eval-cases/case1")[0]?.body).toEqual({ name: "stripe-key-leak!", kind: "must_not_flag" });

    await user.click(within(dialog()).getByRole("button", { name: "Delete case" }));
    expect(onDelete).toHaveBeenCalledTimes(1);
  });

  it("shows no 'Edited' marker on a manual case even when its content was edited", () => {
    mockFetch({});
    const evalCase = makeCase({ origin: "manual", source_finding_id: null, content_edited_at: "2026-10-15T08:00:00.000Z" });
    renderWithProviders(<EvalCaseEditor owner={OWNER} evalCase={evalCase} onClose={() => {}} />);
    expect(within(dialog()).queryByText("Edited")).not.toBeInTheDocument();
    expect(within(dialog()).queryByText("From a finding")).not.toBeInTheDocument();
  });

  it("shows hostile text literally and takes every visible string from the message files", async () => {
    mockFetch({});
    const hostileDiff = `diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,1 +1,2 @@\n x\n+${HOSTILE}\n`;
    const evalCase = makeCase({
      name: HOSTILE,
      notes: HOSTILE,
      origin: "manual",
      source_finding_id: null,
      input_diff: hostileDiff,
      input_meta: { pr_title: HOSTILE, pr_description: HOSTILE, repo: null, pr_number: null, head_sha: null },
      expectation: { kind: "must_find", file: "src/a.ts", start_line: 1, end_line: 2, severity: "WARNING", category: "bug", title: HOSTILE },
    });
    const { container, user } = renderWithProviders(<EvalCaseEditor owner={OWNER} evalCase={evalCase} onClose={() => {}} onDelete={() => {}} />, { messages: pseudoMessages() });
    await waitFor(() => expect(screen.getByText(`${HOSTILE}`, { exact: false, selector: "pre, span, div" })).toBeInTheDocument());
    expect(container.querySelector("img")).toBeNull();
    expect(document.querySelector("img")).toBeNull();
    await user.click(screen.getByRole("button", { name: /eval\.caseEditor\.tabs\.prMeta/ }));
    expect(within(dialog()).getByLabelText(/prTitle/)).toHaveValue(HOSTILE);
    expect(document.querySelector("img")).toBeNull();
    const allowed = [HOSTILE, "src/a.ts", "x", "+", "−", /^\d+$/, /<img/, "@@ -1,1 +1,2 @@"];
    expect(strayText(dialog(), allowed)).toEqual([]);
  });
});
