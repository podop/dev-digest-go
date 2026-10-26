import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { PluginBadge } from "./index";

afterEach(cleanup);

describe("PluginBadge", () => {
  it("renders nothing for an item no plugin owns", () => {
    const { rerender } = renderWithProviders(<PluginBadge />);
    expect(screen.queryByText(/plugin/i)).toBeNull();
    rerender(<PluginBadge plugin={null} />);
    expect(screen.queryByText(/plugin/i)).toBeNull();
  });

  it("labels an enabled plugin's item and shows no disabled marker", () => {
    renderWithProviders(<PluginBadge plugin={{ id: "p1", name: "acme", enabled: true }} />);
    expect(screen.getByText("From plugin acme")).toBeInTheDocument();
    expect(screen.queryByText("plugin disabled")).toBeNull();
  });

  it("adds a plain-text 'plugin disabled' marker when the plugin is off", () => {
    renderWithProviders(<PluginBadge plugin={{ id: "p2", name: "beta", enabled: false }} />);
    expect(screen.getByText("From plugin beta")).toBeInTheDocument();
    expect(screen.getByText("plugin disabled")).toBeInTheDocument();
  });

  it("carries the full label as a native title for a long (truncated) name", () => {
    const name = "a-very-long-plugin-name-".repeat(6);
    renderWithProviders(<PluginBadge plugin={{ id: "p3", name, enabled: true }} />);
    expect(screen.getByTitle(`From plugin ${name}`)).toBeInTheDocument();
  });

  it("takes every visible string from the message files (pseudo bundle)", () => {
    const { container } = renderWithProviders(<PluginBadge plugin={{ id: "p2", name: "beta", enabled: false }} />, {
      messages: pseudoMessages(),
    });
    expect(strayText(container)).toEqual([]);
    expect(screen.getByText("⟦pluginBadge.label⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦pluginBadge.disabled⟧")).toBeInTheDocument();
  });
});
