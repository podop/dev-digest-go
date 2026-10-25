import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, it, expect, afterEach } from "vitest";
import { About, ConnTestResult, Settings, SettingsUpdate, SpendStatus } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { aboutKeys, settingsKeys } from "./keys";
import { useAbout, useSpend, useUpdateSettings } from "./core";

afterEach(cleanup);

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
