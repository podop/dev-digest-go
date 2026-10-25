/* SettingsView — the sub-nav in FR1 order with the current section active, the panel of each
   section (panels of slices A and B, "coming later" for the rest) and the no-hard-coded-copy check
   (AC1, AC22). AppShell is a passthrough; the section panels have their own tests and are
   replaced by markers here. */
import type React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";

vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children, crumb }: { children: React.ReactNode; crumb: { label: string }[] }) => (
    <>
      <ol data-testid="crumb">
        {crumb.map((c) => (
          <li key={c.label}>{c.label}</li>
        ))}
      </ol>
      {children}
    </>
  ),
}));
vi.mock("./_components/SettingsApiKeys", () => ({ SettingsApiKeys: () => <div data-testid="panel-api-keys" /> }));
vi.mock("./_components/SettingsGithub", () => ({ SettingsGithub: () => <div data-testid="panel-github" /> }));
vi.mock("./_components/SettingsWorkspace", () => ({ SettingsWorkspace: () => <div data-testid="panel-workspace" /> }));
vi.mock("./_components/SettingsModels", () => ({ SettingsModels: () => <div data-testid="panel-models" /> }));
vi.mock("./_components/SettingsAbout", () => ({ SettingsAbout: () => <div data-testid="panel-about" /> }));

import { SettingsView } from "./SettingsView";
import { SETTINGS_SECTION_KEYS, type SettingsSectionKey } from "./constants";

afterEach(cleanup);

const LABELS: Record<SettingsSectionKey, string> = {
  "api-keys": "API Keys",
  github: "GitHub Integration",
  workspace: "Workspace",
  "automatic-reviews": "Automatic Reviews",
  integrations: "Integrations",
  models: "Feature Models",
  about: "About",
};
const PANEL_TESTID: Partial<Record<SettingsSectionKey, string>> = {
  "api-keys": "panel-api-keys",
  github: "panel-github",
  workspace: "panel-workspace",
  models: "panel-models",
  about: "panel-about",
};

describe("SettingsView", () => {
  it("lists the seven sections in order, linked to /settings/<key>", () => {
    renderWithProviders(<SettingsView section="api-keys" />);

    const nav = screen.getByRole("navigation", { name: "Settings sections" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((a) => a.textContent)).toEqual([
      "API Keys",
      "GitHub Integration",
      "Workspace",
      "Automatic Reviews",
      "Integrations",
      "Feature Models",
      "About",
    ]);
    expect(links.map((a) => a.getAttribute("href"))).toEqual(SETTINGS_SECTION_KEYS.map((k) => `/settings/${k}`));
  });

  it.each(SETTINGS_SECTION_KEYS)("%s: marks its item active and shows its panel or the empty state", (key) => {
    renderWithProviders(<SettingsView section={key} />);

    const current = within(screen.getByRole("navigation")).getAllByRole("link", { current: "page" });
    expect(current.map((a) => a.textContent)).toEqual([LABELS[key]]);
    expect(within(screen.getByTestId("crumb")).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Settings",
      LABELS[key],
    ]);

    const testid = PANEL_TESTID[key];
    if (testid) {
      expect(screen.getByTestId(testid)).toBeInTheDocument();
      expect(screen.queryByText("This section is coming in a later release.")).not.toBeInTheDocument();
      expect(screen.queryByText(/A6 finalizes/)).not.toBeInTheDocument();
    } else {
      // Sub-nav item, breadcrumb and the empty-state title all carry the label.
      expect(screen.getAllByText(LABELS[key])).toHaveLength(3);
      expect(screen.getByText("This section is coming in a later release.")).toBeInTheDocument();
      expect(document.querySelector("[data-testid^='panel-']")).toBeNull();
    }
  });

  it.each(SETTINGS_SECTION_KEYS)("%s: renders no hard-coded copy", (key) => {
    const { container } = renderWithProviders(<SettingsView section={key} />, { messages: pseudoMessages() });

    expect(strayText(container)).toEqual([]);
  });
});
