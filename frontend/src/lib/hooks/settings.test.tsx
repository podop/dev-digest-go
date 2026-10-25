import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, it, expect, afterEach, vi } from "vitest";
import {
  About,
  AUTO_REVIEW_MAX_AGENTS,
  AutomaticReviews,
  AutomaticReviewsInput,
  AutomaticReviewsStatus,
  ConnTestResult,
  RunHistoryItem,
  RunTrace,
  Settings,
  SettingsUpdate,
  SpendStatus,
} from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { aboutKeys, settingsKeys } from "./keys";
import { AUTO_REVIEWS_STATUS_REFETCH_MS, useAbout, useAutomaticReviewsStatus, useSpend, useUpdateSettings } from "./core";

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

const SPEND = {
  month_start: "2026-10-01T00:00:00.000Z",
  spent_usd: 12.345678,
  cap_usd: 50,
  remaining_usd: 37.654322,
  cap_reached: false,
};

const ABOUT = {
  version: "dev",
  build_id: "dev",
  runner: { kind: "go", version: "1" },
  config_format: "1",
  data_dir: "/data",
  secrets_file: "/data/secrets.env",
};

describe("settings hooks", () => {
  it("useSpend reads GET /settings/spend and a saved cap refetches it", async () => {
    const api = mockFetch({
      "GET /settings/spend": SPEND,
      "PUT /settings": { monthly_spend_cap_usd: 80 },
    });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(
      () => ({ spend: useSpend(), update: useUpdateSettings() }),
      { queryClient: qc },
    );

    await waitFor(() => expect(result.current.spend.data).toEqual(SPEND));
    expect(qc.getQueryState(settingsKeys.spend())?.isInvalidated).toBe(false);

    await act(() => result.current.update.mutateAsync({ monthly_spend_cap_usd: 80 }));

    await waitFor(() => expect(api.requests("GET", "/settings/spend").length).toBeGreaterThan(1));
    expect(qc.getQueryData(settingsKeys.current())).toEqual({ monthly_spend_cap_usd: 80 });
  });

  it("useAbout reads GET /about once and keeps it cached", async () => {
    const api = mockFetch({ "GET /about": ABOUT });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(() => useAbout(), { queryClient: qc });

    await waitFor(() => expect(result.current.data).toEqual(ABOUT));
    expect(qc.getQueryData(aboutKeys.all)).toEqual(ABOUT);
    expect(api.requests("GET", "/about")).toHaveLength(1);
  });
});

const AUTO_STATUS = {
  state: "active",
  last_poll_at: "2026-10-25T10:00:00.000Z",
  prs_tracked: 7,
  poll_interval_min: 5,
};
const AGENT_ID = "6f1c2c9e-4a54-4f0b-8d5e-0c2f6d9b7a11";

describe("automatic reviews hooks", () => {
  it("useAutomaticReviewsStatus reads the status route and a saved setting refetches it", async () => {
    const api = mockFetch({
      "GET /settings/automatic-reviews-status": AUTO_STATUS,
      "PUT /settings": { automatic_reviews: { enabled: true } },
    });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(
      () => ({ status: useAutomaticReviewsStatus(), update: useUpdateSettings() }),
      { queryClient: qc },
    );

    await waitFor(() => expect(result.current.status.data).toEqual(AUTO_STATUS));
    expect(qc.getQueryData(settingsKeys.autoReviewsStatus())).toEqual(AUTO_STATUS);
    expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(1);

    await act(() => result.current.update.mutateAsync({ automatic_reviews: { enabled: true } }));

    await waitFor(() =>
      expect(api.requests("GET", "/settings/automatic-reviews-status").length).toBeGreaterThan(1),
    );
  });

  it("useAutomaticReviewsStatus polls every 30 s while mounted", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = mockFetch({ "GET /settings/automatic-reviews-status": AUTO_STATUS });
    const { result, unmount } = renderHookWithProviders(() => useAutomaticReviewsStatus());

    await waitFor(() => expect(result.current.data).toEqual(AUTO_STATUS));
    expect(AUTO_REVIEWS_STATUS_REFETCH_MS).toBe(30_000);
    expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(1);

    await act(() => vi.advanceTimersByTimeAsync(AUTO_REVIEWS_STATUS_REFETCH_MS + 100));
    await waitFor(() =>
      expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(2),
    );
    unmount();
  });
});

describe("automatic reviews contract", () => {
  const defaults = { enabled: false, agent_ids: [], on_new_pr: true, on_new_commits: true, enabled_at: null };

  it("a stored boolean or a missing value reads as the full object (FR1)", () => {
    expect(Settings.parse({}).automatic_reviews).toEqual(defaults);
    expect(Settings.parse({ automatic_reviews: true }).automatic_reviews).toEqual({ ...defaults, enabled: true });
    expect(Settings.parse({ automatic_reviews: false }).automatic_reviews).toEqual(defaults);
    const full = {
      enabled: true,
      agent_ids: [AGENT_ID],
      on_new_pr: false,
      on_new_commits: true,
      enabled_at: "2026-10-25T10:00:00.000Z",
    };
    expect(Settings.parse({ automatic_reviews: full }).automatic_reviews).toEqual(full);
    expect(AutomaticReviews.parse({ enabled: true }).on_new_pr).toBe(true);
    expect(AutomaticReviews.safeParse("yes").success).toBe(false);
  });

  it("the PUT input is a boolean or a partial without enabled_at, at most 20 uuids (FR2)", () => {
    for (const ok of [true, false, {}, { enabled: true }, { agent_ids: [AGENT_ID], on_new_pr: false }]) {
      expect(AutomaticReviewsInput.safeParse(ok).success, JSON.stringify(ok)).toBe(true);
      expect(SettingsUpdate.safeParse({ automatic_reviews: ok }).success, JSON.stringify(ok)).toBe(true);
    }
    const tooMany = Array.from({ length: AUTO_REVIEW_MAX_AGENTS + 1 }, (_, i) =>
      `6f1c2c9e-4a54-4f0b-8d5e-${String(i).padStart(12, "0")}`,
    );
    for (const bad of ["on", 1, null, { enabled: "yes" }, { agent_ids: ["nope"] }, { agent_ids: tooMany }]) {
      expect(SettingsUpdate.safeParse({ automatic_reviews: bad }).success, JSON.stringify(bad)).toBe(false);
    }
    expect(AUTO_REVIEW_MAX_AGENTS).toBe(20);
    // Absent key stays absent: a PUT of another key must not send automatic_reviews.
    expect(SettingsUpdate.parse({ theme: "light" })).toEqual({ theme: "light" });
    // enabled_at is dropped from the input.
    expect(AutomaticReviewsInput.parse({ enabled: true, enabled_at: "x" })).toEqual({ enabled: true });
  });

  it("AutomaticReviewsStatus parses the seven states and rejects others", () => {
    for (const state of ["off", "poll_disabled", "no_github_token", "no_agents", "no_triggers", "paused_spend_cap", "active"]) {
      expect(AutomaticReviewsStatus.safeParse({ ...AUTO_STATUS, state }).success, state).toBe(true);
    }
    expect(AutomaticReviewsStatus.parse({ ...AUTO_STATUS, last_poll_at: null }).last_poll_at).toBeNull();
    expect(AutomaticReviewsStatus.safeParse({ ...AUTO_STATUS, state: "paused" }).success).toBe(false);
  });

  it("run source auto is accepted in the trace config and the run history (FR11)", () => {
    const item = {
      run_id: "r1",
      ran_at: "2026-10-25T10:00:00.000Z",
      status: "done",
      source: "auto",
      pr: null,
      tokens: null,
      cost_usd: null,
      findings_count: null,
      has_trace: true,
    };
    expect(RunHistoryItem.parse(item).source).toBe("auto");
    expect(RunHistoryItem.safeParse({ ...item, source: "cron" }).success).toBe(false);
    const config = RunTrace.shape.config;
    expect(config.parse({ agent: "a", model: "m", source: "auto" }).source).toBe("auto");
    expect(config.parse({ agent: "a", model: "m" }).source).toBe("local");
  });
});

describe("platform contract additions", () => {
  it("Settings fills default_model and the cap with null, and keeps existing keys", () => {
    const parsed = Settings.parse({ polling_interval_min: 7 });
    expect(parsed.default_model).toBeNull();
    expect(parsed.monthly_spend_cap_usd).toBeNull();
    expect(parsed.polling_interval_min).toBe(7);
  });

  it("the spend cap accepts 0..1e6 with two decimals and rejects the rest", () => {
    for (const ok of [0, 50, 50.5, 12.34, 1_000_000, null]) {
      expect(SettingsUpdate.safeParse({ monthly_spend_cap_usd: ok }).success, String(ok)).toBe(true);
    }
    for (const bad of [-1, 1_000_000.01, 12.345, "50", Number.NaN]) {
      expect(SettingsUpdate.safeParse({ monthly_spend_cap_usd: bad }).success, String(bad)).toBe(false);
    }
  });

  it("default_model is a provider+model pair or null", () => {
    const pair = { provider: "openrouter", model: "a/b" };
    expect(SettingsUpdate.parse({ default_model: pair }).default_model).toEqual(pair);
    expect(SettingsUpdate.parse({ default_model: null }).default_model).toBeNull();
    expect(SettingsUpdate.safeParse({ default_model: { provider: "openrouter", model: "" } }).success).toBe(false);
    expect(SettingsUpdate.safeParse({ default_model: { provider: "github", model: "x" } }).success).toBe(false);
  });

  it("ConnTestResult accepts the old shape and the new optional fields (absent or null)", () => {
    const old = { provider: "openai", ok: true, message: "ok" };
    expect(ConnTestResult.strict().safeParse(old).success).toBe(true);
    const llm = { ...old, latency_ms: 142, model: "m", model_reachable: true };
    expect(ConnTestResult.strict().safeParse(llm).success).toBe(true);
    const failed = { ...old, ok: false, latency_ms: null, model: null, model_reachable: null };
    expect(ConnTestResult.strict().safeParse(failed).success).toBe(true);
    const gh = { provider: "github", ok: true, message: "ok", login: "octo", scopes: [] };
    expect(ConnTestResult.strict().safeParse(gh).success).toBe(true);
    expect(ConnTestResult.strict().safeParse({ ...gh, scopes: null }).success).toBe(true);
  });

  it("SpendStatus and About parse their §7 shapes", () => {
    expect(SpendStatus.parse(SPEND)).toEqual(SPEND);
    expect(SpendStatus.parse({ ...SPEND, cap_usd: null, remaining_usd: null }).cap_usd).toBeNull();
    expect(About.parse(ABOUT)).toEqual(ABOUT);
  });

  it("constants/settings stays zod-free", () => {
    const src = readFileSync(resolve(process.cwd(), "src/vendor/shared/constants/settings.ts"), "utf8");
    expect(src).not.toMatch(/from\s+['"]zod['"]|from\s+['"]\.\.\/contracts/);
  });
});
