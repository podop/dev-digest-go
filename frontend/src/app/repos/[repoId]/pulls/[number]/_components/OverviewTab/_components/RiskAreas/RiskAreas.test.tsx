/* RiskAreas — risk chips read from the stored brief: coloured severity icon, title, ref;
   the chevron button expands the explanation; refs open Files changed at the parsed line. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { PrBrief, PrBriefResponse, Risk } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { useOpenInDiff } from "../../useOpenInDiff";
import { RiskAreas } from "./RiskAreas";

const nav = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: nav.push }) }));

afterEach(() => {
  cleanup();
  nav.push.mockClear();
});

const LONG_PATH = `src/${"deep/".repeat(58)}file.t`; // 300 chars

const RISKS: Risk[] = [
  { kind: "security", title: "Limiter bypass", explanation: "Header can be spoofed.", severity: "high", file_refs: ["src/b.ts:40-52"] },
  { kind: "perf", title: "Hot path alloc", explanation: "Allocates per request.", severity: "medium", file_refs: ["src/a.ts:12", "src/gone.ts:5"] },
  { kind: "style", title: "Naming", explanation: "<i>Rename</i> it.", severity: "low", file_refs: [] },
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
const briefWith = (risks: Risk[]): PrBriefResponse => ({ brief: { ...BRIEF, risks: { risks } }, stale: false });

function Harness() {
  const links = useOpenInDiff({ repoId: "r1", number: "7", changedPaths: ["src/a.ts", "src/b.ts"] });
  return <RiskAreas prId="pr-1" {...links} />;
}

const renderRisks = async (risks: Risk[] = RISKS) => {
  mockFetch({ "GET /pulls/pr-1/brief": briefWith(risks) });
  const view = renderWithProviders(<Harness />);
  await screen.findByRole("heading", { name: "Risk areas" });
  return view;
};

describe("RiskAreas", () => {
  it("shows a coloured severity icon and title per risk and toggles the explanation by keyboard", async () => {
    const { user } = await renderRisks();

    expect(screen.getByRole("img", { name: "High" })).toHaveStyle({ color: "var(--crit)" });
    expect(screen.getByRole("img", { name: "Medium" })).toHaveStyle({ color: "var(--warn)" });
    expect(screen.getByRole("img", { name: "Low" })).toHaveStyle({ color: "var(--text-muted)" });
    expect(screen.getByText("Limiter bypass")).toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: "Limiter bypass" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Header can be spoofed.")).toBeNull();

    toggle.focus();
    await user.keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Header can be spoofed.")).toBeVisible();

    await user.keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Header can be spoofed.")).toBeNull();

    // Space works too and the explanation is plain text, never markup.
    const low = screen.getByRole("button", { name: "Naming" });
    low.focus();
    await user.keyboard(" ");
    expect(screen.getByText("<i>Rename</i> it.")).toBeInTheDocument();
  });

  it("expands two identical risks independently", async () => {
    const twin: Risk = { kind: "perf", title: "Same risk", explanation: "Same words.", severity: "low", file_refs: ["src/a.ts:1"] };
    const { user } = await renderRisks([twin, { ...twin }]);

    const [first, second] = screen.getAllByRole("button", { name: "Same risk" });
    await user.click(first as HTMLElement);
    expect(first).toHaveAttribute("aria-expanded", "true");
    expect(second).toHaveAttribute("aria-expanded", "false");
    expect(screen.getAllByText("Same words.")).toHaveLength(1);
  });

  it("opens a ref with its line range, and reports a ref outside the diff inline", async () => {
    const { user } = await renderRisks();

    await user.click(screen.getByRole("button", { name: "src/b.ts:40-52" }));
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls/7?tab=diff&file=src%2Fb.ts&line=40-52");

    await user.click(screen.getByRole("button", { name: "src/gone.ts:5" }));
    expect(nav.push).toHaveBeenCalledOnce();
    expect(screen.getByText("File not in this PR's diff")).toBeInTheDocument();
  });

  it("reaches every ref and chevron button by Tab and shows the no-risks text when empty", async () => {
    const { user, unmount } = await renderRisks();
    const order = ["src/b.ts:40-52", "Limiter bypass", "src/a.ts:12", "src/gone.ts:5", "Hot path alloc", "Naming"];
    for (const name of order) {
      await user.tab();
      expect(screen.getByRole("button", { name })).toHaveFocus();
    }
    unmount();

    await renderRisks([]);
    expect(screen.getByText("No notable risks flagged.")).toBeInTheDocument();
  });

  it("truncates a 300-char path with ellipsis and keeps the full path as its tooltip", async () => {
    const longRef = `${LONG_PATH}:7`;
    await renderRisks([{ kind: "perf", title: "Deep path", explanation: "Long.", severity: "low", file_refs: [longRef] }]);

    expect(LONG_PATH).toHaveLength(300);
    const ref = screen.getByTitle(longRef);
    expect(ref).toHaveTextContent(longRef);
    expect(ref).toHaveStyle({ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: "100%" });
  });

  it("renders nothing while there is no brief, even with no skeleton", async () => {
    const api = mockFetch({ "GET /pulls/pr-1/brief": { brief: null, stale: false } satisfies PrBriefResponse });
    renderWithProviders(<Harness />);
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/brief")).toHaveLength(1));
    expect(screen.queryByRole("heading", { name: "Risk areas" })).toBeNull();
    expect(screen.queryByRole("list")).toBeNull();
  });
});
