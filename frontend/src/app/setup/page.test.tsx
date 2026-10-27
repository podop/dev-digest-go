/* /setup route entry — a server redirect to Settings › First-run setup that keeps the step query. */
import { describe, it, expect, beforeEach, vi } from "vitest";

const redirect = vi.hoisted(() =>
  vi.fn((url: string) => {
    throw new Error(`NEXT_REDIRECT ${url}`);
  }),
);
vi.mock("next/navigation", () => ({ redirect }));

import SetupPage from "./page";

beforeEach(() => {
  redirect.mockClear();
});

describe("SetupPage", () => {
  it.each([
    ["model", "/settings/setup?step=model"],
    ["code-host", "/settings/setup?step=code-host"],
    ["github", "/settings/setup?step=github"],
    [["repo", "model"], "/settings/setup?step=repo"],
    ["a&b=c", "/settings/setup?step=a%26b%3Dc"],
    [undefined, "/settings/setup"],
  ])("redirects step %j to %s", async (step, to) => {
    await expect(SetupPage({ searchParams: Promise.resolve({ step }) })).rejects.toThrow(`NEXT_REDIRECT ${to}`);
    expect(redirect).toHaveBeenCalledWith(to);
  });
});
