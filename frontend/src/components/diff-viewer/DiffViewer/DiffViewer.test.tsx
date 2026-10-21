import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "@/test/render";
import { DiffViewer } from "./DiffViewer";
import type { DiffFindingApi, DiffFindingItem } from "../findings";
import type { PrFile } from "@/lib/types";

const PATCH = `@@ -1,3 +1,3 @@
 context
-removed line
+added line`;
const FILE: PrFile = { path: "a.ts", additions: 1, deletions: 1, patch: PATCH };

// No line follows the deletion, so the new file has no line 2 at all — a
// finding anchored there can never match a rendered (RIGHT) line.
const DELETE_ONLY_PATCH = `@@ -1,2 +1,1 @@
 context
-removed line`;
const DELETE_ONLY_FILE: PrFile = { path: "a.ts", additions: 0, deletions: 1, patch: DELETE_ONLY_PATCH };

function findingApi(items: DiffFindingItem[], show = true): DiffFindingApi {
  return {
    items,
    flagged: new Set(items.map((i) => i.file)),
    show,
    renderCard: (item) => <div data-testid={`card-${item.id}`}>{item.id}</div>,
  };
}

describe("DiffViewer findings slot", () => {
  it("renders the card under its line and the severity label", () => {
    renderWithProviders(
      <DiffViewer
        files={[FILE]}
        findingApi={findingApi([{ id: "f1", file: "a.ts", start_line: 2, severity: "CRITICAL" }])}
      />,
    );
    expect(screen.getByTestId("card-f1")).toBeInTheDocument();
    expect(screen.getByText("blocker")).toBeInTheDocument();
  });

  it("clicking the severity badge hides that line's card, clicking again shows it", async () => {
    const { user } = renderWithProviders(
      <DiffViewer
        files={[FILE]}
        findingApi={findingApi([{ id: "f1", file: "a.ts", start_line: 2, severity: "SUGGESTION" }])}
      />,
    );
    const badge = screen.getByRole("button", { name: "suggestion" });
    expect(badge).toHaveAttribute("aria-expanded", "true");

    await user.click(badge);
    expect(screen.queryByTestId("card-f1")).not.toBeInTheDocument();
    expect(badge).toHaveAttribute("aria-expanded", "false");

    await user.click(badge);
    expect(screen.getByTestId("card-f1")).toBeInTheDocument();
  });

  it("puts a finding anchored to a deleted (old-side) line into the unmatched block", () => {
    renderWithProviders(
      <DiffViewer
        files={[DELETE_ONLY_FILE]}
        findingApi={findingApi([{ id: "f2", file: "a.ts", start_line: 2, severity: "WARNING" }])}
      />,
    );
    // Line 2 was deleted and nothing replaces it — no RIGHT:2 to anchor on.
    expect(screen.getByText("Findings not shown inline")).toBeInTheDocument();
    expect(screen.getByTestId("card-f2")).toBeInTheDocument();
  });

  it("keeps the severity label visible even when show=false hides the card", () => {
    renderWithProviders(
      <DiffViewer
        files={[FILE]}
        findingApi={findingApi([{ id: "f3", file: "a.ts", start_line: 2, severity: "SUGGESTION" }], false)}
      />,
    );
    expect(screen.getByText("suggestion")).toBeInTheDocument();
    expect(screen.queryByTestId("card-f3")).not.toBeInTheDocument();
  });

  it("shows a dot on a flagged file's header", () => {
    renderWithProviders(
      <DiffViewer
        files={[FILE]}
        findingApi={findingApi([{ id: "f4", file: "a.ts", start_line: 2, severity: "CRITICAL" }])}
      />,
    );
    expect(screen.getByLabelText("Has findings")).toBeInTheDocument();
  });
});

describe("DiffViewer highlight (a finding's file:line deep-link)", () => {
  // Over AUTO_EXPAND_MAX_LINES: this file would start collapsed without a highlight.
  const BIG: PrFile = {
    path: "big.ts",
    additions: 500,
    deletions: 0,
    patch: "@@ -1,2 +1,4 @@\n ctx one\n+line two\n+line three\n ctx four",
  };
  const scroll = vi.fn();
  const original = Element.prototype.scrollIntoView;
  beforeEach(() => {
    scroll.mockClear();
    Element.prototype.scrollIntoView = scroll; // jsdom has no scrollIntoView
  });
  afterEach(() => {
    Element.prototype.scrollIntoView = original;
  });

  it("opens the file, marks the range and scrolls to its first line once", () => {
    const { rerender } = renderWithProviders(
      <DiffViewer files={[BIG, FILE]} highlight={{ path: "big.ts", start: 2, end: 3 }} />,
    );
    const marked = document.querySelectorAll("[data-highlighted]");
    expect([...marked].map((el) => el.textContent)).toEqual([
      expect.stringContaining("line two"),
      expect.stringContaining("line three"),
    ]);
    expect(scroll).toHaveBeenCalledOnce();
    expect(scroll.mock.contexts[0]).toBe(marked[0]);

    // A re-render (data refresh) must not yank the page back.
    rerender(<DiffViewer files={[BIG, FILE]} highlight={{ path: "big.ts", start: 2, end: 3 }} />);
    expect(scroll).toHaveBeenCalledOnce();
  });

  it("scrolls to the file card when the range isn't in the patch", () => {
    renderWithProviders(<DiffViewer files={[BIG]} highlight={{ path: "big.ts", start: 90, end: 95 }} />);
    expect(document.querySelectorAll("[data-highlighted]")).toHaveLength(0);
    expect(scroll).toHaveBeenCalledOnce();
    expect(scroll.mock.contexts[0]).toHaveTextContent("big.ts");
  });

  it("leaves other files alone", () => {
    renderWithProviders(<DiffViewer files={[BIG]} highlight={{ path: "other.ts", start: 2, end: 2 }} />);
    expect(screen.queryByText("line two")).not.toBeInTheDocument();
    expect(scroll).not.toHaveBeenCalled();
  });
});

