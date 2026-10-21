import { describe, it, expect } from "vitest";
import { s } from "./styles";

/* Plan Decisions (docs/plans/2026-09-25-themed-select.md): "Use CSS variables
   only. Every token used exists in both [data-theme] blocks [F2]." F2 confirms
   these tokens are defined in BOTH vendor/ui/styles.css:10 ([data-theme="dark"])
   and :49 ([data-theme="light"]) — so any of them stays legible in either theme;
   a literal hex/rgb color would not. */
const KNOWN_TOKENS = ["--bg-elevated", "--bg-hover", "--border-strong", "--text-primary", "--text-muted", "--shadow-modal"];

const HEX_OR_RGB = /#[0-9a-fA-F]{3,8}|rgba?\(/;

function colorBearingValues(): string[] {
  const pos = { left: 0, width: 0, top: 0, maxHeight: 0 };
  return [
    s.trigger(false).background as string,
    s.trigger(false).border as string,
    s.trigger(true).border as string,
    s.value(false).color as string,
    s.value(true).color as string,
    s.chevron.color as string,
    s.popup(pos).background as string,
    s.popup(pos).border as string,
    s.popup(pos).boxShadow as string,
    s.option(false).background as string,
    s.option(true).background as string,
    s.option(false).color as string,
    s.check(false).color as string,
    s.check(true).color as string,
  ];
}

describe("select styles use CSS variables only, never a hard-coded color", () => {
  it("no trigger/popup/option/check color is a literal hex or rgb() value", () => {
    for (const value of colorBearingValues()) {
      expect(value).not.toMatch(HEX_OR_RGB);
    }
  });

  it("every var(--token) referenced is one confirmed to exist in both theme blocks", () => {
    const found = new Set<string>();
    for (const value of colorBearingValues()) {
      for (const match of value.matchAll(/var\((--[\w-]+)\)/g)) found.add(match[1]!);
    }
    expect(found.size).toBeGreaterThan(0);
    for (const token of found) {
      expect(KNOWN_TOKENS).toContain(token);
    }
  });
});
