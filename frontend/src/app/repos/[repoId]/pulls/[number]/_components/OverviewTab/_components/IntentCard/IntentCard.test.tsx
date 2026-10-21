/* IntentCard — quoted intent, in/out-of-scope columns, empty state, through the
   real TanStack hooks with a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { PrIntentResponse } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { IntentCard } from "./IntentCard";

const INTENT: PrIntentResponse = {
  stale: false,
  intent: {
    intent: "Add rate limiting to public API endpoints.",
    in_scope: ["Add middleware", "Return 429"],
    out_of_scope: ["Authentication changes"],
    change_type: "feature",
    confidence: "high",
    derived_from: "explicit",
    sources: [],
    pr_id: "pr-1",
    head_sha: "abc",
    input_hash: "h",
    prompt_version: 1,
    provider: "p",
    model: "m-1",
    tokens_in: 1,
    tokens_out: 1,
    cost_usd: 0.01,
    derived_at: "2026-02-01T12:00:00Z",
  },
};

afterEach(cleanup);

describe("IntentCard", () => {
  it("shows the intent as a quote with in-scope and out-of-scope columns", async () => {
    mockFetch({ "GET /pulls/pr-1/intent": INTENT });
    renderWithProviders(<IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc" />);

    const card = await screen.findByRole("region", { name: "PR intent" });
    expect(within(card).getByText("“Add rate limiting to public API endpoints.”")).toBeInTheDocument();
    expect(within(card).getByText("In scope")).toBeInTheDocument();
    expect(within(card).getByText("Out of scope")).toBeInTheDocument();
    const items = within(card).getAllByRole("listitem").map((li) => li.textContent);
    expect(items).toEqual(["·Add middleware", "·Return 429", "·Authentication changes"]);
    expect(within(card).getByText("high confidence")).toBeInTheDocument();
  });

  it("omits an empty scope column", async () => {
    mockFetch({ "GET /pulls/pr-1/intent": { ...INTENT, intent: { ...INTENT.intent!, in_scope: [] } } });
    renderWithProviders(<IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc" />);

    const card = await screen.findByRole("region", { name: "PR intent" });
    expect(within(card).queryByText("In scope")).toBeNull();
    expect(within(card).getByText("Out of scope")).toBeInTheDocument();
  });

  it("offers a Derive-now call to action before the first derivation", async () => {
    mockFetch({ "GET /pulls/pr-1/intent": { intent: null, stale: false } satisfies PrIntentResponse });
    renderWithProviders(<IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc" />);
    expect(await screen.findByRole("button", { name: /derive/i })).toBeInTheDocument();
  });

  it("renders its children slot under the scope columns, also before the first derivation", async () => {
    mockFetch({ "GET /pulls/pr-1/intent": INTENT });
    const first = renderWithProviders(
      <IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc">
        <p>Slot content</p>
      </IntentCard>,
    );
    const card = await screen.findByRole("region", { name: "PR intent" });
    const slot = within(card).getByText("Slot content");
    expect(within(card).getByText("Out of scope").compareDocumentPosition(slot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    first.unmount();

    mockFetch({ "GET /pulls/pr-1/intent": { intent: null, stale: false } satisfies PrIntentResponse });
    renderWithProviders(
      <IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc">
        <p>Slot content</p>
      </IntentCard>,
    );
    expect(await screen.findByRole("button", { name: /derive/i })).toBeInTheDocument();
    expect(screen.getByText("Slot content")).toBeInTheDocument();
  });

  it("still renders its children slot when loading the intent fails", async () => {
    mockFetch({ "GET /pulls/pr-1/intent": jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    renderWithProviders(
      <IntentCard prId="pr-1" repoFullName="acme/api" headSha="abc">
        <p>Slot content</p>
      </IntentCard>,
    );
    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(screen.getByText("Slot content")).toBeInTheDocument();
  });
});
