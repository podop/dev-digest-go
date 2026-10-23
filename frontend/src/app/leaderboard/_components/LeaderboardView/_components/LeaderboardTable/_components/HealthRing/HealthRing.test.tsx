import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { HealthRing } from "./HealthRing";

afterEach(cleanup);

describe("HealthRing", () => {
  it("prints the number and colours it by the 85 / 60 bands", () => {
    const stroke = (health: number) => {
      const { container, unmount } = renderWithProviders(<HealthRing health={health} />);
      const color = container.querySelector("[data-ring]")?.getAttribute("stroke");
      unmount();
      return color;
    };
    expect(stroke(100)).toBe("var(--ok)");
    expect(stroke(85)).toBe("var(--ok)");
    expect(stroke(84)).toBe("var(--warn)");
    expect(stroke(60)).toBe("var(--warn)");
    expect(stroke(59)).toBe("var(--crit)");
    expect(stroke(0)).toBe("var(--crit)");

    renderWithProviders(<HealthRing health={72} />);
    expect(screen.getByText("72")).toBeInTheDocument();
  });
});
