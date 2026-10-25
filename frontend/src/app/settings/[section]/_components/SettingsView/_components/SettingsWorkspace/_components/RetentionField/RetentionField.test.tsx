import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { RetentionField } from "./RetentionField";

afterEach(cleanup);

const HINT =
  "Run traces older than this are pruned on startup and once a day. Runs, reviews and findings are kept.";
const NOT_VALID = "Stored value is not valid — nothing is pruned until you choose one";

const box = () => screen.getByRole("combobox", { name: "Retention" });

function mockSettings(settings: Record<string, unknown>, put: unknown = (req: { body: unknown }) => req.body) {
  return mockFetch({
    "GET /settings": settings,
    "PUT /settings": put as never,
  });
}

describe("RetentionField", () => {
  it("shows 90 days when unset with six options, saves Keep forever as null (FR10, AC10)", async () => {
    const api = mockSettings({});
    const { user } = renderWithProviders(<RetentionField />);

    expect(await screen.findByText(HINT)).toBeInTheDocument();
    expect(box()).toHaveTextContent("90 days");
    expect(screen.queryByText(NOT_VALID)).not.toBeInTheDocument();

    await user.click(box());
    const labels = screen.getAllByRole("option").map((o) => o.textContent);
    expect(labels).toEqual(["30 days", "60 days", "90 days", "180 days", "365 days", "Keep forever"]);
    await user.click(screen.getByRole("option", { name: "Keep forever" }));

    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({ retention_days: null });
    await waitFor(() => expect(box()).toHaveTextContent("Keep forever"));

    await pickOption(user, box(), "30 days");
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(2));
    expect(api.requests("PUT", "/settings")[1]!.body).toEqual({ retention_days: 30 });
    await waitFor(() => expect(box()).toHaveTextContent("30 days"));
  });

  it("shows a stored null as Keep forever", async () => {
    mockSettings({ retention_days: null });
    renderWithProviders(<RetentionField />);

    await waitFor(() => expect(box()).toHaveTextContent("Keep forever"));
    expect(screen.queryByText(NOT_VALID)).not.toBeInTheDocument();
  });

  it("restores the previous value and shows an inline error when the save fails (AC10)", async () => {
    const api = mockSettings(
      { retention_days: 180 },
      jsonResponse({ error: { code: "validation_error", message: "invalid" } }, 422),
    );
    const { user } = renderWithProviders(<RetentionField />);

    await waitFor(() => expect(box()).toHaveTextContent("180 days"));
    await pickOption(user, box(), "30 days");

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the retention setting.");
    expect(api.requests("PUT", "/settings")).toHaveLength(1);
    expect(box()).toHaveTextContent("180 days");
  });

  it("notes a stored value that is not an allowed one and selects nothing until one is chosen (§9)", async () => {
    mockSettings({ retention_days: 45 });
    const { user } = renderWithProviders(<RetentionField />);

    expect(await screen.findByText(NOT_VALID)).toBeInTheDocument();
    expect(box()).toHaveTextContent("Choose…");
    await user.click(box());
    for (const option of screen.getAllByRole("option")) {
      expect(option).not.toHaveAttribute("aria-selected", "true");
    }
    await user.click(screen.getByRole("option", { name: "60 days" }));

    await waitFor(() => expect(box()).toHaveTextContent("60 days"));
    await waitFor(() => expect(screen.queryByText(NOT_VALID)).not.toBeInTheDocument());
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    mockSettings({ retention_days: 45 });
    const { container } = renderWithProviders(<RetentionField />, { messages: pseudoMessages() });

    await screen.findByText(/workspaceRetention\.invalid/);
    expect(strayText(container)).toEqual([]);
    expect(within(container).getByText(/workspaceRetention\.hint/)).toBeInTheDocument();
  });
});
