/* EvalCaseRow — status, name, expectation, and the manual / edited markers. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { makeCase } from "@/test/eval-fixtures";
import { EvalCaseRow } from "./EvalCaseRow";

afterEach(cleanup);

const renderRow = (over: Parameters<typeof makeCase>[0], handlers = { onOpen: vi.fn(), onDelete: vi.fn() }) =>
  renderWithProviders(
    <ul>
      <EvalCaseRow evalCase={makeCase(over)} {...handlers} />
    </ul>,
  );

describe("EvalCaseRow", () => {
  it("tags a manual agent case and an edited finding case, opens and deletes through the callbacks", async () => {
    const handlers = { onOpen: vi.fn(), onDelete: vi.fn() };
    const { user } = renderRow({ origin: "finding", content_edited_at: "2026-10-15T08:00:00.000Z" }, handlers);
    const row = screen.getByTestId("eval-case-row");
    expect(within(row).getByText("edited")).toBeInTheDocument();
    expect(within(row).queryByText("manual")).not.toBeInTheDocument();
    expect(within(row).getByText("never run")).toBeInTheDocument();

    await user.click(within(row).getByRole("button", { name: "stripe-key-leak" }));
    await user.click(within(row).getByRole("button", { name: "Delete case stripe-key-leak" }));
    expect(handlers.onOpen).toHaveBeenCalledTimes(1);
    expect(handlers.onDelete).toHaveBeenCalledTimes(1);
  });

  it("shows neither marker on an unedited finding case; a manual case shows manual but never edited", () => {
    renderRow({ origin: "finding", content_edited_at: null });
    expect(screen.queryByText("manual")).not.toBeInTheDocument();
    expect(screen.queryByText("edited")).not.toBeInTheDocument();
    cleanup();
    renderRow({ origin: "manual", content_edited_at: "2026-10-15T08:00:00.000Z" });
    expect(screen.getByText("manual")).toBeInTheDocument();
    expect(screen.queryByText("edited")).not.toBeInTheDocument();
    cleanup();
    renderRow({ origin: "manual", owner_kind: "skill", owner_id: "sk1", agent_id: null });
    expect(screen.queryByText("manual")).not.toBeInTheDocument();
  });
});
