import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { otherMetricChanges, othersSentence } from "./helpers";

const alert = { metrics: [{ metric: "precision" as const, delta: -0.02 }] };

describe("regression banner helpers", () => {
  it("lists the metrics that did not dip with their direction, skipping undefined ones", () => {
    const previous = makeRun({ recall: 0.78, precision: 0.93, citation_accuracy: 0.94 });
    const latest = makeRun({ recall: 0.82, precision: 0.91, citation_accuracy: 0.94 });
    expect(otherMetricChanges(alert, latest, previous)).toEqual([
      { metric: "recall", direction: "up" },
      { metric: "citation_accuracy", direction: "flat" },
    ]);
    expect(otherMetricChanges(alert, makeRun({ recall: null }), previous).map((c) => c.metric)).toEqual(["citation_accuracy"]);
    expect(otherMetricChanges(alert, latest, null)).toEqual([]);
  });

  it("a small drop that is not an alert metric reads down", () => {
    const changes = otherMetricChanges(alert, makeRun({ recall: 0.81 }), makeRun({ recall: 0.82, precision: 0.93 }));
    expect(changes[0]).toEqual({ metric: "recall", direction: "down" });
  });

  it("shapes the sentence by how many metrics remain and whether they move alike", () => {
    const up = { metric: "recall" as const, direction: "up" as const };
    const flat = { metric: "citation_accuracy" as const, direction: "flat" as const };
    expect(othersSentence([]).kind).toBe("none");
    expect(othersSentence([up]).kind).toBe("one");
    expect(othersSentence([up, { ...flat, direction: "up" }]).kind).toBe("both");
    expect(othersSentence([up, flat]).kind).toBe("mixed");
  });
});
