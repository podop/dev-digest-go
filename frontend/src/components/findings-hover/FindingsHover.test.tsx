import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import type { FindingRecord } from "@devdigest/shared";
import { FindingsHover } from "./FindingsHover";
import { countsFromMap, lineRef, plainText } from "./helpers";

afterEach(cleanup);

const renderHover = (ui: React.ReactElement) => renderWithProviders(ui);

describe("findings-hover helpers", () => {
  it("countsFromMap drops zero severities and keeps display order", () => {
    expect(countsFromMap({ SUGGESTION: 2, CRITICAL: 1, WARNING: 0 })).toEqual([
      { severity: "CRITICAL", count: 1 },
      { severity: "SUGGESTION", count: 2 },
    ]);
    expect(countsFromMap(null)).toEqual([]);
  });

  it("lineRef / plainText format the preview", () => {
    expect(lineRef({ file: "a.ts", start_line: 3, end_line: 3 })).toBe("a.ts:3");
    expect(lineRef({ file: "a.ts", start_line: 3, end_line: 9 })).toBe("a.ts:3-9");
    expect(plainText("**bold** `code`")).toBe("bold code");
  });
});

describe("FindingsHover", () => {
  it("renders nothing without counts", () => {
    renderHover(
      <div data-testid="host">
        <FindingsHover counts={[]} items={[]} />
      </div>,
    );
    expect(screen.getByTestId("host")).toBeEmptyDOMElement();
  });

  it("shows a loading popover and calls onShow on hover", async () => {
    const onShow = vi.fn();
    const { user } = renderHover(
      <FindingsHover counts={[{ severity: "WARNING", count: 3 }]} items={undefined} loading onShow={onShow} />,
    );
    await user.hover(screen.getByLabelText("3 warning"));
    expect(onShow).toHaveBeenCalledOnce();
    expect(screen.getByRole("tooltip")).toHaveTextContent("3 findings in this run");
    expect(screen.getByRole("tooltip")).toHaveTextContent("Loading findings…");
  });

  // jsdom has no layout (and user-event sends mouseout without relatedTarget), so the
  // pointer path can't be replayed — pin the fix instead: the offset from the counters is
  // padding inside the hover target; a margin gap fired mouseleave and closed the popover.
  it("bridges the counters-to-popover gap with padding, not margin", async () => {
    const { user } = renderHover(<FindingsHover counts={[{ severity: "WARNING", count: 3 }]} items={undefined} loading />);
    await user.hover(screen.getByLabelText("3 warning"));
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip).toHaveStyle({ paddingTop: "8px" });
    expect(tooltip.style.marginTop).toBe("");
  });

  it("links each finding's file:line when findingHref is given", async () => {
    const f = {
      id: "f1", severity: "WARNING", category: "bug", title: "T", file: "src/a.ts", start_line: 3, end_line: 5,
      rationale: "r", suggestion: null, confidence: 0.9, kind: "finding", trifecta_components: null,
      evidence: null, review_id: "rv", accepted_at: null, dismissed_at: null,
    } as FindingRecord;
    const { user } = renderHover(
      <FindingsHover counts={[{ severity: "WARNING", count: 1 }]} items={[f]} findingHref={(x) => `/to/${x.id}`} />,
    );
    await user.hover(screen.getByLabelText("1 warning"));
    expect(screen.getByRole("link", { name: "src/a.ts:3-5" })).toHaveAttribute("href", "/to/f1");
  });
});

