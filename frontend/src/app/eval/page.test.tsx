/* /eval route entry — the document title comes from messages/en (no hard-coded copy);
   ?agent= selects the single-agent view. */
import { describe, it, expect, vi } from "vitest";
import { messages, pseudoMessages } from "@/test/render";

const bundle = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));
vi.mock("next-intl/server", () => ({
  getTranslations: async (ns: string) => (key: string) => {
    const value = key.split(".").reduce<unknown>((o, k) => (o as Record<string, unknown>)[k], bundle.current[ns]);
    return value as string;
  },
}));

import EvalPage, { generateMetadata } from "./page";
import { EvalDashboardView } from "./_components/EvalDashboardView";

describe("EvalPage", () => {
  it("takes the title from the en messages", async () => {
    bundle.current = pseudoMessages(messages) as Record<string, unknown>;
    expect(await generateMetadata()).toEqual({ title: "⟦eval.page.metaTitle⟧" });
    bundle.current = messages as Record<string, unknown>;
    expect(await generateMetadata()).toEqual({ title: "Eval Dashboard" });
  });

  it("renders the dashboard view for the resolved ?agent= and ?period=", async () => {
    const page = await EvalPage({ searchParams: Promise.resolve({ agent: "ag1", period: "7d" }) });
    expect(page.type).toBe(EvalDashboardView);
    expect(page.props).toEqual({ agentId: "ag1", period: "7d" });
  });

  it("falls back to 30d for a missing or unknown ?period=", async () => {
    const missing = await EvalPage({ searchParams: Promise.resolve({ agent: "ag1" }) });
    expect(missing.props).toEqual({ agentId: "ag1", period: "30d" });
    const bogus = await EvalPage({ searchParams: Promise.resolve({ period: "bogus" }) });
    expect(bogus.props).toEqual({ agentId: null, period: "30d" });
  });
});
