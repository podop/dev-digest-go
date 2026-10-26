import { describe, it, expect } from "vitest";
import { draftsFromModels, isValidPrice, modelProblems, modelsToInput, type ModelDraft } from "./helpers";

const draft = (over: Partial<ModelDraft>): ModelDraft => ({ key: "k", id: "m", label: "", prompt: "", completion: "", ...over });

describe("manual model helpers", () => {
  it("accepts prices from 0 to 10,000 with at most 6 decimals (FR1)", () => {
    for (const ok of ["0", "2", "8.5", " 0.000001 ", "10000"]) expect(isValidPrice(ok), ok).toBe(true);
    for (const bad of ["abc", "", "-1", "1.1234567", "10000.01", "1e3", "1,5", ".5"]) expect(isValidPrice(bad), bad).toBe(false);
  });

  it("flags an empty or duplicate id, a bad price and a half-filled pair", () => {
    const problems = modelProblems([
      draft({ id: "a", prompt: "2", completion: "8" }),
      draft({ id: "a" }),
      draft({ id: "", prompt: "abc", completion: "1" }),
      draft({ id: "b", prompt: "3" }),
    ]);
    expect(problems).toEqual([{}, { id: "idDuplicate" }, { id: "idRequired", prompt: "priceInvalid" }, { completion: "priceBoth" }]);
  });

  it("builds the request list: pricing only when both prices are typed, label null when blank", () => {
    const out = modelsToInput([draft({ id: " m1 ", label: " Big ", prompt: "2", completion: "8.5" }), draft({ id: "m2" })]);
    expect(out).toEqual([
      { id: "m1", label: "Big", pricing: { promptPerM: 2, completionPerM: 8.5 } },
      { id: "m2", label: null, pricing: null },
    ]);
  });

  it("round-trips a stored list into drafts", () => {
    const drafts = draftsFromModels([{ id: "m1", label: null, pricing: { promptPerM: 1.5, completionPerM: 6 } }]);
    expect(drafts.map(({ key: _k, ...rest }) => rest)).toEqual([{ id: "m1", label: "", prompt: "1.5", completion: "6" }]);
    expect(modelsToInput(drafts)).toEqual([{ id: "m1", label: null, pricing: { promptPerM: 1.5, completionPerM: 6 } }]);
  });
});
