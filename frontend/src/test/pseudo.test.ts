import { describe, it, expect, afterEach } from "vitest";
import { strayText } from "./pseudo";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("strayText", () => {
  it("ignores markers and allowed data, reports everything else", () => {
    document.body.innerHTML = "<div><b>⟦a.b⟧</b> <i>Hard coded</i> <span>mock/model</span> <u>⟦x⟧ 5%</u></div>";
    expect(strayText(document.body, ["mock/model"])).toEqual(["Hard coded", "5%"]);
    expect(strayText(document.body, [/model/, "Hard coded", /5%/])).toEqual([]);
  });
});
