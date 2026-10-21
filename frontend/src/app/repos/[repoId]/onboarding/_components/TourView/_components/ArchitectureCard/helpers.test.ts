import { describe, it, expect } from "vitest";
import type { OnboardingEdge, OnboardingNode } from "@devdigest/shared";
import { layoutDiagram, shortenLabel, splitInlineCode } from "./helpers";

const node = (id: string): OnboardingNode => ({ id, label: id, kind: "module" });

describe("splitInlineCode", () => {
  it("splits backtick spans and leaves a lone backtick as text", () => {
    expect(splitInlineCode("a `b` c")).toEqual([
      { text: "a ", code: false },
      { text: "b", code: true },
      { text: " c", code: false },
    ]);
    expect(splitInlineCode("odd ` tick")).toEqual([{ text: "odd ` tick", code: false }]);
  });
});

describe("layoutDiagram", () => {
  it("layers nodes by longest path, left to right", () => {
    const edges: OnboardingEdge[] = [
      { from: "a", to: "b" },
      { from: "b", to: "c" },
      { from: "a", to: "c" },
    ];
    const l = layoutDiagram([node("a"), node("b"), node("c")], edges);
    const x = (id: string) => l?.boxes.find((b) => b.id === id)?.x ?? -1;
    expect(x("a")).toBeLessThan(x("b"));
    expect(x("b")).toBeLessThan(x("c"));
    expect(l?.arrows).toHaveLength(3);
  });

  it("returns null below 2 distinct nodes", () => {
    expect(layoutDiagram([node("a"), node("a")], [])).toBeNull();
    expect(layoutDiagram([], [])).toBeNull();
  });

  it("survives cycles, self loops and edges to unknown ids", () => {
    const l = layoutDiagram(
      [node("a"), node("b")],
      [
        { from: "a", to: "b" },
        { from: "b", to: "a" },
        { from: "a", to: "a" },
        { from: "a", to: "ghost" },
      ],
    );
    expect(l?.boxes).toHaveLength(2);
    expect(l?.arrows).toHaveLength(2);
  });

  it("cuts long labels for the box", () => {
    expect(shortenLabel("x".repeat(40))).toHaveLength(18);
    expect(shortenLabel("short")).toBe("short");
  });
});
