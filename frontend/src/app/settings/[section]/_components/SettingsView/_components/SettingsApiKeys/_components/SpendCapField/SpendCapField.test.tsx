import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SpendCapField } from "./SpendCapField";

afterEach(cleanup);

const MONTH = "2026-10-01T00:00:00.000Z";
const spend = (over: Record<string, unknown> = {}) => ({
  month_start: MONTH,
  spent_usd: 18.4,
  cap_usd: 50,
  remaining_usd: 31.6,
  cap_reached: false,
  ...over,
});

/** GET /settings serves `state.cap`; PUT /settings stores it, like the backend. */
function mockCap(cap: number | null, spendBody: unknown = spend()) {
  const state = { cap };
  const api = mockFetch({
    "GET /settings": () => ({ monthly_spend_cap_usd: state.cap }),
    "GET /settings/spend": () => spendBody,
    "PUT /settings": (req) => {
      state.cap = (req.body as { monthly_spend_cap_usd: number | null }).monthly_spend_cap_usd;
      return { monthly_spend_cap_usd: state.cap };
    },
  });
  return api;
}

const input = () => screen.getByRole("textbox", { name: "Monthly spend cap in USD" });
const saveButton = () => screen.getByRole("button", { name: "Save" });

describe("SpendCapField", () => {
  it("shows the month's spend and saves a typed amount, stripping $ and clearing to null (AC12, EC18)", async () => {
    const api = mockCap(50);
    const { user } = renderWithProviders(<SpendCapField />);

    expect(await screen.findByText(/\$18\.40 used this month/)).toBeInTheDocument();
    expect(screen.getByText("$31.60 remaining of $50.00")).toBeInTheDocument();
    await waitFor(() => expect(input()).toHaveValue("50"));
    expect(saveButton()).toBeDisabled();

    await user.clear(input());
    await user.type(input(), "$60");
    await user.click(saveButton());
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({ monthly_spend_cap_usd: 60 });
    await waitFor(() => expect(input()).toHaveValue("60"));
    expect(saveButton()).toBeDisabled();

    await user.clear(input());
    await user.click(saveButton());
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(2));
    expect(api.requests("PUT", "/settings")[1]!.body).toEqual({ monthly_spend_cap_usd: null });
  });

  it("blocks non-numeric, 3-decimal and out-of-range input with an inline error and no request (EC18)", async () => {
    const api = mockCap(null, spend({ cap_usd: null, remaining_usd: null }));
    const { user } = renderWithProviders(<SpendCapField />);
    expect(await screen.findByText("No cap · $18.40 used this month")).toBeInTheDocument();

    await user.type(input(), "abc");
    expect(screen.getByRole("alert")).toHaveTextContent("Enter an amount in dollars");
    expect(input()).toHaveAccessibleDescription(/Enter an amount in dollars/);
    expect(input()).toBeInvalid();
    expect(saveButton()).toBeDisabled();
    await user.keyboard("{Enter}");

    await user.clear(input());
    await user.type(input(), "50.123");
    expect(screen.getByRole("alert")).toHaveTextContent("Use at most 2 decimals.");

    await user.clear(input());
    await user.type(input(), "1000001");
    expect(screen.getByRole("alert")).toHaveTextContent("Enter an amount between $0 and $1,000,000.");

    await user.clear(input());
    await user.type(input(), "0");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(saveButton()).toBeEnabled();
    expect(api.requests("PUT", "/settings")).toHaveLength(0);
  });

  it("warns when the cap is reached", async () => {
    mockCap(50, spend({ spent_usd: 50.12, remaining_usd: 0, cap_reached: true }));
    renderWithProviders(<SpendCapField />);

    expect(
      await screen.findByText(/Monthly spend cap of \$50\.00 reached: \$50\.12 used since 2026-10-01 \(UTC\)/),
    ).toBeInTheDocument();
    expect(screen.getByText("$0.00 remaining of $50.00")).toBeInTheDocument();
  });

  it("adds the unknown-cost sentence with plural forms, with or without a cap, nothing for 0 or absent (AC16)", async () => {
    const sentence = /calls? with unknown cost (is|are) not included\./;
    const view = (body: unknown) => {
      mockCap(50, body);
      return renderWithProviders(<SpendCapField />);
    };

    let r = view(spend({ unpriced_count: 1 }));
    expect(await screen.findByText("1 call with unknown cost is not included.")).toBeInTheDocument();
    expect(screen.getByText(/\$18\.40 used this month/)).toBeInTheDocument();
    r.unmount();

    r = view(spend({ unpriced_count: 3, cap_usd: null, remaining_usd: null }));
    expect(await screen.findByText("3 calls with unknown cost are not included.")).toBeInTheDocument();
    expect(screen.getByText(/No cap · \$18\.40 used this month/)).toBeInTheDocument();
    r.unmount();

    for (const over of [{ unpriced_count: 0 }, {}]) {
      r = view(spend(over));
      expect(await screen.findByText(/\$18\.40 used this month/)).toBeInTheDocument();
      expect(screen.queryByText(sentence)).not.toBeInTheDocument();
      r.unmount();
    }
  });

  it("says spend is unavailable and retries the read", async () => {
    let fail = true;
    const api = mockFetch({
      "GET /settings": { monthly_spend_cap_usd: null },
      "GET /settings/spend": () =>
        fail ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : spend({ cap_usd: null, remaining_usd: null }),
    });
    const { user } = renderWithProviders(<SpendCapField />);

    expect(await screen.findByText("Spend unavailable")).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No cap · $18.40 used this month")).toBeInTheDocument();
    expect(screen.queryByText("Spend unavailable")).not.toBeInTheDocument();
    expect(api.requests("GET", "/settings/spend")).toHaveLength(2);
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    mockCap(50, spend({ spent_usd: 50.12, remaining_usd: 0, cap_reached: true }));
    const { container } = renderWithProviders(<SpendCapField />, { messages: pseudoMessages() });

    await screen.findByText(/settingsBudget\.spendCap\.reached/);
    expect(strayText(container)).toEqual([]);
  });

  it("renders the unknown-cost sentence without hard-coded copy under pseudo messages (AC16)", async () => {
    mockCap(50, spend({ unpriced_count: 3 }));
    const { container } = renderWithProviders(<SpendCapField />, { messages: pseudoMessages() });

    await screen.findByText(/settingsBudget\.spendCap\.unpriced/);
    expect(strayText(container)).toEqual([]);
  });
});
