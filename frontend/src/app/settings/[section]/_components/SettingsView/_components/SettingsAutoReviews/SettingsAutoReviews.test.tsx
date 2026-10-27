/* SettingsAutoReviews — the section composition (FR13, FR18), the controls' saves and failures
   (AC17, AC19, FR20), the info box link and the no-hard-coded-copy check (AC21), accessible
   names (NFR6). The leaves have their own tests; here the real hooks run against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { Agent } from "@devdigest/shared";
import type { AutomaticReviews, AutomaticReviewsStatus } from "@/lib/types";
import { createAppQueryClient } from "@/lib/providers";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { SettingsAutoReviews } from "./SettingsAutoReviews";

afterEach(cleanup);

const A1 = "11111111-1111-4111-8111-111111111111";
const A2 = "22222222-2222-4222-8222-222222222222";
const STATUS_ROUTE = "GET /settings/automatic-reviews-status";

function agent(id: string, name: string, enabled = true): Agent {
  return {
    id,
    name,
    description: "",
    provider: "anthropic",
    model: "m",
    system_prompt: "p",
    enabled,
    version: 1,
    strategy: "single-pass",
    ci_fail_on: "critical",
    repo_intel: true,
  };
}

function automatic(over: Partial<AutomaticReviews> = {}): AutomaticReviews {
  return { enabled: false, agent_ids: [], on_new_pr: true, on_new_commits: true, enabled_at: null, ...over };
}

const STATUS: AutomaticReviewsStatus = { state: "off", last_poll_at: null, prs_tracked: 0, poll_interval_min: 5 };

/** Settings and agents routes; PUT echoes the stored object merged with the patch unless overridden. */
function stub(settings: Record<string, unknown>, extra: Record<string, never | unknown> = {}) {
  return mockFetch({
    "GET /settings": settings,
    "GET /agents": [agent(A1, "Security"), agent(A2, "Style")],
    [STATUS_ROUTE]: STATUS,
    "GET /repos": [],
    ...extra,
  } as never);
}

const puts = (api: ReturnType<typeof mockFetch>) => api.requests("PUT", "/settings").map((r) => r.body);
const sw = () => screen.getByRole("switch", { name: "Auto-run review on new PR detection" });
const interval = () => screen.getByRole("combobox", { name: "Polling interval" });
const box = (name: string) => screen.getByRole("checkbox", { name });

describe("SettingsAutoReviews", () => {
  it("names the code hosts in the subtitle once a GitLab repository exists (GitLab A FR13)", async () => {
    stub(
      { polling_interval_min: 5, automatic_reviews: automatic() },
      {
        "GET /repos": [
          { id: "r1", full_name: "acme/web", provider: "github" },
          { id: "r2", full_name: "acme/platform/api", provider: "gitlab", host: "gitlab.com" },
        ],
      },
    );
    renderWithProviders(<SettingsAutoReviews />);

    expect(
      await screen.findByText("Poll your code hosts for new PRs and run agents without manual triggering."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Poll GitHub for new PRs and run agents without manual triggering.")).not.toBeInTheDocument();
  });

  it("names the GitLab CI review in the info box once a GitLab repository exists (GitLab C FR47, AC42)", async () => {
    const stored = { polling_interval_min: 5, automatic_reviews: automatic() };
    stub(stored, { "GET /repos": [{ id: "r2", full_name: "acme/platform/api", provider: "gitlab", host: "gitlab.com" }] });
    renderWithProviders(<SettingsAutoReviews />);

    const link = await screen.findByRole("link", { name: "Integrations" });
    await waitFor(() =>
      expect(link.parentElement).toHaveTextContent(
        "Polling is the default. For instant triggers and CI execution, install the GitHub Action or GitLab CI review under Integrations.",
      ),
    );
    expect(link).toHaveAttribute("href", "/settings/integrations");
  });

  it("composes title, toggle, interval, chips, triggers, status and the Integrations link; each control saves its own patch (FR13, FR18, FR20, AC17, NFR6)", async () => {
    // Like the server: the answer carries the full stored object with the patch merged in.
    let stored: Record<string, unknown> = { polling_interval_min: 7, automatic_reviews: automatic() };
    const api = stub(stored, {
      "PUT /settings": (req: { body: Record<string, unknown> }) => {
        const { automatic_reviews, ...rest } = req.body;
        stored = { ...stored, ...rest, automatic_reviews: { ...(stored.automatic_reviews as object), ...(automatic_reviews as object) } };
        return stored;
      },
    });
    const { user } = renderWithProviders(<SettingsAutoReviews />);

    expect(await screen.findByRole("heading", { name: "Automatic Reviews" })).toBeInTheDocument();
    expect(screen.getByText("Poll GitHub for new PRs and run agents without manual triggering.")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Security" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "Style" })).toBeInTheDocument();
    expect(sw()).toHaveAttribute("aria-checked", "false");
    expect(interval()).toHaveTextContent("Every 7 minutes");
    expect(box("On new PR")).toBeChecked();
    expect(box("On new commits to existing PR")).toBeChecked();
    expect(await screen.findByText("Off — PRs are reviewed only when you start a review")).toBeInTheDocument();

    // The info box: the text with "Integrations" linking to the section.
    const link = screen.getByRole("link", { name: "Integrations" });
    expect(link).toHaveAttribute("href", "/settings/integrations");
    expect(link.parentElement).toHaveTextContent(
      "Polling is the default. For instant triggers and CI execution, install the GitHub Action under Integrations.",
    );

    // The status line sits under the trigger checkboxes.
    const triggers = screen.getByRole("group", { name: "Trigger conditions" });
    const statusLine = screen.getByText("Off — PRs are reviewed only when you start a review");
    expect(triggers.compareDocumentPosition(statusLine) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    await user.click(sw());
    await waitFor(() => expect(puts(api)).toHaveLength(1));
    expect(puts(api)[0]).toEqual({ automatic_reviews: { enabled: true } });
    await waitFor(() => expect(sw()).toHaveAttribute("aria-checked", "true"));

    await pickOption(user, interval(), "Every 10 minutes");
    await waitFor(() => expect(puts(api)).toHaveLength(2));
    expect(puts(api)[1]).toEqual({ polling_interval_min: 10 });

    await user.click(screen.getByRole("button", { name: "Security" }));
    await waitFor(() => expect(puts(api)).toHaveLength(3));
    expect(puts(api)[2]).toEqual({ automatic_reviews: { agent_ids: [A1] } });

    await user.click(box("On new commits to existing PR"));
    await waitFor(() => expect(puts(api)).toHaveLength(4));
    expect(puts(api)[3]).toEqual({ automatic_reviews: { on_new_commits: false } });
  });

  it("disables the controls while saving, then a failed PUT returns the control to the stored value and toasts the message (AC19, EC18)", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = stub({ polling_interval_min: 5, automatic_reviews: automatic() }, {
      "PUT /settings": async () => {
        await gate;
        return jsonResponse({ error: { code: "invalid_request", message: "Choose at least one agent" } }, 422);
      },
    });
    const { user } = renderWithProviders(<SettingsAutoReviews />, { queryClient: createAppQueryClient() });

    await screen.findByRole("button", { name: "Security" });
    await user.click(box("On new PR"));
    await waitFor(() => expect(box("On new PR")).toBeDisabled());
    expect(sw()).toBeDisabled();
    expect(interval()).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("button", { name: "Security" })).toBeDisabled();
    expect(puts(api)).toEqual([{ automatic_reviews: { on_new_pr: false } }]);

    release();
    expect(await screen.findByText("Choose at least one agent")).toBeInTheDocument();
    await waitFor(() => expect(box("On new PR")).toBeEnabled());
    expect(box("On new PR")).toBeChecked();
    expect(sw()).toBeEnabled();
    expect(sw()).toHaveAttribute("aria-checked", "false");
  });

  it("shows the chips only after the agents loaded, a skeleton before, a Retry on failure", async () => {
    let fail = true;
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    stub({ polling_interval_min: 5, automatic_reviews: automatic({ agent_ids: [A1] }) }, {
      "GET /agents": async () => {
        await gate;
        return fail ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : [agent(A1, "Security")];
      },
    });
    const { user } = renderWithProviders(<SettingsAutoReviews />);

    expect(await screen.findByRole("switch")).toBeInTheDocument();
    expect(screen.getByLabelText("Loading agents…")).toBeInTheDocument();
    expect(screen.queryByText("No agents yet")).not.toBeInTheDocument();

    release();
    const alert = await screen.findByText("Could not load the agents.");
    expect(alert).toBeInTheDocument();
    expect(screen.queryByText("No agents yet")).not.toBeInTheDocument();

    fail = false;
    await user.click(within(alert.closest("div")?.parentElement ?? document.body).getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("button", { name: "Security" })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows a settings load error with Retry", async () => {
    let fail = true;
    stub({}, {
      "GET /settings": () => (fail ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : { automatic_reviews: automatic() }),
    });
    const { user } = renderWithProviders(<SettingsAutoReviews />);

    expect(await screen.findByText("Could not load the automatic review settings.")).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("switch")).toBeInTheDocument();
    expect(interval()).toHaveTextContent("Every 5 minutes");
  });

  it("renders no hard-coded copy (AC21, NFR6)", async () => {
    stub({ polling_interval_min: 5, automatic_reviews: automatic({ enabled: true, agent_ids: [A1] }) }, {
      [STATUS_ROUTE]: { ...STATUS, state: "active", prs_tracked: 3 },
    });
    const { container } = renderWithProviders(<SettingsAutoReviews />, { messages: pseudoMessages() });

    await screen.findByRole("button", { name: /Security/ });
    await screen.findByText(/states\.active/);
    expect(strayText(container, ["Security", "Style", "5", "3"])).toEqual([]);
  });
});
