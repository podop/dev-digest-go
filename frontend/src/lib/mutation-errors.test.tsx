import { describe, it, expect, afterEach } from "vitest";
import { useMutation } from "@tanstack/react-query";
import { cleanup, render, screen, within, userEvent } from "@/test/render";
import { TestProviders, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { api, ApiError } from "./api";
import { createAppQueryClient } from "./providers";
import { isSpendCapError } from "./mutation-errors";
import type { MutationMeta } from "./query-meta";

afterEach(cleanup);

const capBody = {
  error: {
    code: "spend_cap_reached",
    message: "Monthly spend cap of $50.00 reached: $50.12 used since 2026-10-01 (UTC).",
    details: { cap_usd: 50, spent_usd: 50.12, month_start: "2026-10-01T00:00:00.000Z" },
  },
};

function RunButton({ meta }: { meta?: MutationMeta }) {
  const run = useMutation({ mutationFn: () => api.post("/pulls/p1/review"), meta });
  // Mutations never reject to the caller here: the global MutationCache is what is under test.
  return <button type="button" onClick={() => run.mutate()}>Run review</button>;
}

function setup(status: number, body: unknown, meta?: MutationMeta, bundle?: ReturnType<typeof pseudoMessages>) {
  mockFetch({ "POST /pulls/p1/review": () => jsonResponse(body, status) });
  const view = render(
    <TestProviders client={createAppQueryClient()} messages={bundle}>
      <RunButton meta={meta} />
    </TestProviders>,
  );
  return { view, user: userEvent.setup() };
}

describe("global mutation error toast (402 surfacing)", () => {
  it("shows a 402 spend_cap_reached with a link to the spend cap in Settings", async () => {
    const { user } = setup(402, capBody);
    await user.click(screen.getByRole("button", { name: "Run review" }));

    const region = await screen.findByRole("status");
    expect(await within(region).findByText(/Monthly spend cap of \$50\.00 reached: \$50\.12 used/)).toBeInTheDocument();
    const link = within(region).getByRole("link", { name: "Review the spend cap in Settings" });
    expect(link).toHaveAttribute("href", "/settings/api-keys");
  });

  it("still shows a 402 when the mutation silences every error", async () => {
    const { user } = setup(402, capBody, { quietErrorCodes: ["*"] });
    await user.click(screen.getByRole("button", { name: "Run review" }));

    const link = await screen.findByRole("link", { name: "Review the spend cap in Settings" });
    expect(link).toHaveAttribute("href", "/settings/api-keys");
    expect(screen.getByText(/Monthly spend cap of \$50\.00 reached/)).toBeInTheDocument();
  });

  it("leaves other errors as they are: toasted without a link, or silent when the meta says so", async () => {
    const other = { error: { code: "conflict", message: "Review already running" } };
    const loud = setup(409, other);
    await loud.user.click(screen.getByRole("button", { name: "Run review" }));
    expect(await screen.findByText("Review already running")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    loud.view.unmount();

    // A 402 with another code is not a spend-cap refusal: it follows the normal rules.
    const quiet = setup(402, { error: { code: "payment_required", message: "Billing" } }, { quietErrorCodes: ["*"] });
    await quiet.user.click(screen.getByRole("button", { name: "Run review" }));
    await screen.findByRole("button", { name: "Run review" });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Billing")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("renders the link text from messages only (pseudo messages)", async () => {
    const { user } = setup(402, capBody, undefined, pseudoMessages());
    await user.click(screen.getByRole("button", { name: "Run review" }));

    const link = await screen.findByRole("link", { name: "⟦common.toastActions.spendCapSettings⟧" });
    const region = screen.getByRole("status");
    expect(region).toContainElement(link);
    // The server's message is data and the icon glyphs are decorative; the rest must be markers.
    expect(strayText(region, [/^Monthly spend cap/, "✕", "×"])).toEqual([]);
  });
});

describe("isSpendCapError", () => {
  it("needs both the 402 status and the spend_cap_reached code", () => {
    expect(isSpendCapError(new ApiError("m", 402, "spend_cap_reached"))).toBe(true);
    expect(isSpendCapError(new ApiError("m", 402, "other"))).toBe(false);
    expect(isSpendCapError(new ApiError("m", 409, "spend_cap_reached"))).toBe(false);
    expect(isSpendCapError(new Error("m"))).toBe(false);
  });
});
