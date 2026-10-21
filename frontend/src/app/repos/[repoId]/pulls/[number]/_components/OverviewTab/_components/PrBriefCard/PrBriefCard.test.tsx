/* PrBriefCard — empty/generating/error/stale states and the missing-inputs note, through the
   real TanStack hooks with a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { PrBrief, PrBriefResponse, ReviewRecord } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { useGenerateBrief } from "@/lib/hooks";
import { PrBriefCard } from "./PrBriefCard";

afterEach(cleanup);

const BRIEF: PrBrief = {
  summary: "Adds rate limiting to the public API.",
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
  tokens_in: 3000,
  tokens_out: 400,
  cost_usd: 0.01,
  model_requests: 1,
};
const withBrief = (over: Partial<PrBrief> = {}, stale = false): PrBriefResponse => ({ brief: { ...BRIEF, ...over }, stale });
const NO_REVIEWS: ReviewRecord[] = [];

/** The generate mutation lives in OverviewTab; this harness owns it the same way. */
function Harness({ headSha }: { headSha: string | null }) {
  const generate = useGenerateBrief("pr-1");
  return (
    <PrBriefCard prId="pr-1" headSha={headSha} generating={generate.isPending} generateError={generate.error} onGenerate={() => generate.mutate()} />
  );
}

const renderCard = (headSha: string | null = "abc") => renderWithProviders(<Harness headSha={headSha} />);

describe("PrBriefCard", () => {
  it("offers Generate brief, shows a skeleton with disabled button while the POST runs, then the brief", async () => {
    let release!: (r: PrBriefResponse) => void;
    const pending = new Promise<PrBriefResponse>((res) => {
      release = res;
    });
    const api = mockFetch({
      "GET /pulls/pr-1/brief": { brief: null, stale: false },
      "GET /pulls/pr-1/reviews": NO_REVIEWS,
      "POST /pulls/pr-1/brief": () => pending,
    });
    const { user } = renderCard();

    expect(await screen.findByText(/One model call is made with the Risk Brief model/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Brief not available yet." })).toBeInTheDocument();
    const card = screen.getByRole("region", { name: "PR Brief" });
    expect(within(card).getByText("PR Brief")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Generate brief" }));

    await waitFor(() => expect(screen.getByRole("region", { name: "PR Brief" })).toHaveAttribute("aria-busy", "true"));
    expect(screen.getByRole("button", { name: /generate brief/i })).toBeDisabled();
    expect(api.requests("POST", "/pulls/pr-1/brief")).toHaveLength(1);

    release(withBrief());
    expect(await screen.findByText("Adds rate limiting to the public API.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
  });

  it("disables Refresh while a regeneration of an existing brief runs", async () => {
    let release!: (r: PrBriefResponse) => void;
    const pending = new Promise<PrBriefResponse>((res) => {
      release = res;
    });
    const api = mockFetch({
      "GET /pulls/pr-1/brief": withBrief(),
      "GET /pulls/pr-1/reviews": NO_REVIEWS,
      "POST /pulls/pr-1/brief": () => pending,
    });
    const { user } = renderCard();

    await user.click(await screen.findByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /refresh/i })).toBeDisabled());
    expect(api.requests("POST", "/pulls/pr-1/brief")).toHaveLength(1);

    release(withBrief({ summary: "Regenerated." }));
    expect(await screen.findByText("Regenerated.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
  });

  it("shows the previous brief again with the error message when regeneration fails", async () => {
    mockFetch({
      "GET /pulls/pr-1/brief": withBrief(),
      "GET /pulls/pr-1/reviews": NO_REVIEWS,
      "POST /pulls/pr-1/brief": jsonResponse({ error: { code: "generation_failed", message: "Provider timed out" } }, 502),
    });
    const { user } = renderCard();

    await user.click(await screen.findByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not generate the brief: Provider timed out");
    expect(screen.getByText("Adds rate limiting to the public API.")).toBeInTheDocument();
  });

  it("lists every missing input with its reason", async () => {
    mockFetch({
      "GET /pulls/pr-1/brief": withBrief({
        missing_inputs: [
          { input: "intent", reason: "not_derived" },
          { input: "specs", reason: "no_review_run" },
          { input: "specs", reason: "over_budget", detail: "2 of 5" },
        ],
      }),
      "GET /pulls/pr-1/reviews": NO_REVIEWS,
    });
    renderCard();

    expect(await screen.findByText("Generated without: intent (not derived yet)")).toBeInTheDocument();
    expect(screen.getByText("Generated without: spec documents (no review run yet)")).toBeInTheDocument();
    expect(screen.getByText("Generated without: spec documents (over the size budget, 2 of 5)")).toBeInTheDocument();
  });

  it("marks the brief stale on the server flag or on a differing page head SHA, and not otherwise", async () => {
    const api = mockFetch({ "GET /pulls/pr-1/brief": withBrief({}, true), "GET /pulls/pr-1/reviews": NO_REVIEWS });
    const first = renderCard();
    expect(await screen.findByText("New commits since this brief was generated")).toBeInTheDocument();
    first.unmount();

    api.on("GET /pulls/pr-1/brief", withBrief({}, false));
    const second = renderCard("def");
    expect(await screen.findByText("New commits since this brief was generated")).toBeInTheDocument();
    second.unmount();

    renderCard("abc");
    await screen.findByText("Adds rate limiting to the public API.");
    expect(screen.queryByText("New commits since this brief was generated")).toBeNull();
  });

  it("renders the summary as literal text, never as markup", async () => {
    mockFetch({ "GET /pulls/pr-1/brief": withBrief({ summary: "<b>x</b> done" }), "GET /pulls/pr-1/reviews": NO_REVIEWS });
    renderCard();
    expect(await screen.findByText("<b>x</b> done")).toBeInTheDocument();
  });

  it("shows an error state with Retry when the brief cannot be loaded", async () => {
    mockFetch({ "GET /pulls/pr-1/brief": jsonResponse({ error: { code: "boom", message: "Server broke" } }, 500) });
    renderCard();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the brief");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});
