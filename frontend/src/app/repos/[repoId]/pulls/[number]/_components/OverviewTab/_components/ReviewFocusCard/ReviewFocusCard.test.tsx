/* ReviewFocusCard — items in stored order with a count badge; a click opens Files changed as a
   new history entry, or says inline that the file is not in the PR's diff. Hidden without a brief. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { PrBrief, PrBriefResponse, ReviewFocusItem } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { useOpenInDiff } from "../../useOpenInDiff";
import { ReviewFocusCard } from "./ReviewFocusCard";

const nav = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: nav.push }) }));

afterEach(() => {
  cleanup();
  nav.push.mockClear();
});

const LONG_PATH = `src/${"deep/".repeat(58)}file.t`; // 300 chars

const ITEMS: ReviewFocusItem[] = [
  { file: "src/a.ts", line: 12, reason: "Start here: the new limiter." },
  { file: "src/outside/caller.ts", line: 3, reason: "Caller outside the diff." },
];

const BRIEF: PrBrief = {
  summary: "s",
  risks: { risks: [] },
  review_focus: [],
  intent: null,
  blast: null,
  missing_inputs: [],
  specs_used: [],
  head_sha: "abc",
  generated_at: "2026-02-01T12:00:00Z",
  prompt_version: "v1",
  provider: "openai",
  model: "gpt-4.1",
  tokens_in: 1,
  tokens_out: 1,
  cost_usd: 0.01,
  model_requests: 1,
};
const briefWith = (items: ReviewFocusItem[]): PrBriefResponse => ({ brief: { ...BRIEF, review_focus: items }, stale: false });

function Harness() {
  const links = useOpenInDiff({ repoId: "r1", number: "7", changedPaths: ["src/a.ts", "src/b.ts"] });
  return <ReviewFocusCard prId="pr-1" {...links} />;
}

const renderFocus = async (items: ReviewFocusItem[] = ITEMS) => {
  mockFetch({ "GET /pulls/pr-1/brief": briefWith(items) });
  const view = renderWithProviders(<Harness />);
  await screen.findByRole("heading", { name: /Review focus/ });
  return view;
};

describe("ReviewFocusCard", () => {
  it("lists the items in stored order with a count badge, reachable by Tab with accessible names", async () => {
    const { user } = await renderFocus();

    const heading = screen.getByRole("heading", { name: /Review focus — read these first/ });
    expect(within(heading.parentElement as HTMLElement).getByText("2")).toBeInTheDocument();
    const items = screen.getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("▸src/a.ts:12 — Start here: the new limiter.");
    expect(items[1]).toHaveTextContent("▸src/outside/caller.ts:3 — Caller outside the diff.");

    await user.tab();
    expect(screen.getByRole("button", { name: "src/a.ts:12" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "src/outside/caller.ts:3" })).toHaveFocus();
  });

  it("pushes the diff deep-link for a changed file and stays put for a file outside the diff", async () => {
    const { user } = await renderFocus();

    await user.click(screen.getByRole("button", { name: "src/outside/caller.ts:3" }));
    expect(nav.push).not.toHaveBeenCalled();
    expect(screen.getByText("File not in this PR's diff")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "src/a.ts:12" }));
    expect(nav.push).toHaveBeenCalledOnce();
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls/7?tab=diff&file=src%2Fa.ts&line=12");
    expect(screen.queryByText("File not in this PR's diff")).toBeNull();
  });

  it("keeps identical items apart: only the clicked one reports a file outside the diff", async () => {
    const twin: ReviewFocusItem = { file: "src/outside/caller.ts", line: 3, reason: "Caller outside the diff." };
    const { user } = await renderFocus([twin, { ...twin }]);

    await user.click(screen.getAllByRole("button", { name: "src/outside/caller.ts:3" })[1] as HTMLElement);
    expect(screen.getAllByText("File not in this PR's diff")).toHaveLength(1);
    expect(screen.getAllByRole("listitem")[1]).toHaveTextContent("File not in this PR's diff");
  });

  it("shows the empty text without a badge when there are no items, and nothing without a brief", async () => {
    const { unmount } = await renderFocus([]);
    expect(screen.getByText("No focus items.")).toBeInTheDocument();
    expect(screen.queryByText("0")).toBeNull();
    unmount();

    const api = mockFetch({ "GET /pulls/pr-1/brief": { brief: null, stale: false } satisfies PrBriefResponse });
    renderWithProviders(<Harness />);
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/brief")).toHaveLength(1));
    expect(screen.queryByRole("heading", { name: /Review focus/ })).toBeNull();
  });

  it("truncates a 300-char path with ellipsis and keeps the full path as its tooltip", async () => {
    await renderFocus([{ file: LONG_PATH, line: 7, reason: "Long one." }]);

    expect(LONG_PATH).toHaveLength(300);
    const ref = screen.getByTitle(`${LONG_PATH}:7`);
    expect(ref).toHaveTextContent(`${LONG_PATH}:7`);
    expect(ref).toHaveStyle({ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: "100%" });
  });
});
