/* /onboarding route entry — a server redirect to the repository step of Settings › First-run setup
   (AC21, FR28), which moves to an earlier step when the model key or GitHub token is missing. */
import { describe, it, expect, vi } from "vitest";

const redirect = vi.hoisted(() =>
  vi.fn((url: string) => {
    throw new Error(`NEXT_REDIRECT ${url}`);
  }),
);
vi.mock("next/navigation", () => ({ redirect }));

import OnboardingPage from "./page";

describe("OnboardingPage", () => {
  it("redirects to /settings/setup?step=repo and renders nothing itself", () => {
    expect(() => OnboardingPage()).toThrow("NEXT_REDIRECT /settings/setup?step=repo");
    expect(redirect).toHaveBeenCalledWith("/settings/setup?step=repo");
  });
});
