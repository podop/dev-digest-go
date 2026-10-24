/* /settings/:section route entry — a known key renders the view for it; an unknown key
   redirects to the default section (AC1). */
import { describe, it, expect, beforeEach, vi } from "vitest";

const redirect = vi.hoisted(() =>
  vi.fn((url: string) => {
    throw new Error(`NEXT_REDIRECT ${url}`);
  }),
);
vi.mock("next/navigation", () => ({ redirect }));

import SettingsPage from "./page";
import { SettingsView } from "./_components/SettingsView";
import { SETTINGS_SECTION_KEYS } from "./_components/SettingsView/constants";

beforeEach(() => {
  redirect.mockClear();
});

describe("SettingsPage", () => {
  it.each(SETTINGS_SECTION_KEYS)("renders the view for %s without redirecting", async (section) => {
    const page = await SettingsPage({ params: Promise.resolve({ section }) });
    expect(page.type).toBe(SettingsView);
    expect(page.props).toEqual({ section });
    expect(redirect).not.toHaveBeenCalled();
  });

  it.each(["nope", "", "API-KEYS", "api-keys/x"])("redirects the unknown key %j to /settings/api-keys", async (section) => {
    await expect(SettingsPage({ params: Promise.resolve({ section }) })).rejects.toThrow(
      "NEXT_REDIRECT /settings/api-keys",
    );
    expect(redirect).toHaveBeenCalledWith("/settings/api-keys");
  });
});
