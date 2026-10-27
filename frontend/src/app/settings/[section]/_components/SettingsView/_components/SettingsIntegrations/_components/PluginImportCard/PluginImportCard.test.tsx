/* PluginImportCard — FR16, EC14, AC19. The preview and install hooks run for real against a
   stubbed fetch; the raw upload body is read from the fetch mock's init.body (the route table
   only parses string bodies). */
import { describe, it, expect, afterEach } from "vitest";
import { fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PluginImportPreview, PluginImportResult } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { PluginImportCard } from "./PluginImportCard";

afterEach(cleanup);

const NO_CONFLICTS = { plugin: false, skills: [] as string[] };

function makePreview(overrides: Partial<PluginImportPreview> = {}): PluginImportPreview {
  return {
    manifest: {
      name: "acme-reviews",
      version: "1.2.0",
      format: "devdigest-plugin/v1",
      exported_at: "2026-10-26T10:00:00.000Z",
      description: "Acme review pack",
      counts: { agents: 2, skills: 2, eval_cases: 3, conventions: 1, learnings: 4 },
    },
    kind: "plugin",
    counts: { agents: 2, skills: 2, eval_cases: 3, conventions: 1, learnings: 4 },
    agents: [
      { name: "Security reviewer", provider: "openai", model: "gpt-5.4", enabled: true, skills: ["owasp-top-10", "secrets"] },
      { name: "Style reviewer", provider: "openai", model: "gpt-5.4", enabled: true, skills: [] },
    ],
    skills: [
      { name: "owasp-top-10", type: "security", description: "OWASP", enabled: true },
      { name: "secrets", type: "security", description: "Secrets", enabled: true },
    ],
    conflicts: NO_CONFLICTS,
    ignored: [{ path_or_section: "README.md", reason: "Not part of the plugin" }],
    warnings: ["Agent Style reviewer uses model gpt-5.4, which is not configured"],
    ...overrides,
  };
}

const RESULT: PluginImportResult = {
  installed: {
    id: "11111111-1111-4111-8111-111111111111",
    name: "acme-reviews",
    version: "1.2.0",
    source: "acme.zip",
    installed_at: "2026-10-26T10:05:00.000Z",
    enabled: false,
    description: "Acme review pack",
    counts: { agents: 2, skills: 2, eval_cases: 3, learnings: 4 },
  },
  created: { agents: 2, skills: 2, eval_cases: 3, conventions: 0, learnings: 4 },
};

function zip(name = "acme.zip") {
  return new File(["PK-zip-bytes"], name, { type: "application/zip" });
}

/** The import requests the page sent, with the raw init. */
function importCalls(api: ReturnType<typeof mockFetch>) {
  return api.fetch.mock.calls
    .map(([input, init]) => ({ url: new URL(String(input)), init: init as RequestInit }))
    .filter((c) => c.url.pathname === "/plugins/import");
}

/** `applyAccept: false` so a rejected type reaches the component (user.upload would drop it otherwise). */
function uploader() {
  return userEvent.setup({ applyAccept: false });
}

function fileInput(label = "Plugin file") {
  return screen.getByLabelText(label);
}

describe("PluginImportCard", () => {
  it("refuses a .tar or .md file with the type message and sends no request (EC14)", async () => {
    const api = mockFetch({});
    renderWithProviders(<PluginImportCard />);
    const user = uploader();

    await user.upload(fileInput(), new File(["x"], "plugin.tar"));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose a .zip or .json plugin file");

    // A drop goes through the same check.
    fireEvent.drop(screen.getByRole("group", { name: "Plugin file drop zone" }), {
      dataTransfer: { files: [new File(["x"], "notes.md")] },
    });
    expect(screen.getByRole("alert")).toHaveTextContent("Choose a .zip or .json plugin file");
    expect(api.fetch).not.toHaveBeenCalled();
    // The zone stays.
    expect(screen.getByRole("button", { name: "browse files…" })).toBeInTheDocument();
  });

  it("refuses an oversized zip on the client without a request", async () => {
    const api = mockFetch({});
    renderWithProviders(<PluginImportCard />);
    const big = zip("big.zip");
    Object.defineProperty(big, "size", { value: 5 * 1024 * 1024 + 1 });

    await uploader().upload(fileInput(), big);
    expect(await screen.findByRole("alert")).toHaveTextContent("This file is larger than 5 MB");
    expect(api.fetch).not.toHaveBeenCalled();
  });

  it("previews a zip (Checking…, counts, names, ignored, warnings) and installs the same file, then shows the enable hint", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = mockFetch({
      "POST /plugins/import": async (req) => {
        if (req.search.includes("dry_run=true")) {
          await gate;
          return makePreview();
        }
        return jsonResponse(RESULT, 201);
      },
    });
    const { user } = renderWithProviders(<PluginImportCard />);
    const file = zip();

    await uploader().upload(fileInput(), file);
    expect(await screen.findByText("Checking…")).toBeInTheDocument();
    release();

    const review = await screen.findByRole("region", { name: /Review plugin/ });
    expect(within(review).getByText("acme-reviews")).toHaveAttribute("title", "acme-reviews");
    expect(review).toHaveTextContent("2 agents · 2 skills · 3 eval cases · 4 learnings");
    expect(review).toHaveTextContent("1 convention is in the file but never installed.");
    expect(within(review).getByText("Security reviewer")).toBeInTheDocument();
    expect(within(review).getByText("Skills: owasp-top-10, secrets")).toBeInTheDocument();
    expect(within(review).getByText("No skills linked")).toBeInTheDocument();
    expect(within(review).getByText("owasp-top-10", { selector: "span" })).toBeInTheDocument();
    expect(within(review).getByText("README.md — Not part of the plugin")).toBeInTheDocument();
    expect(within(review).getByText(/which is not configured/)).toBeInTheDocument();
    expect(review).toHaveFocus();
    const install = within(review).getByRole("button", { name: "Install" });
    expect(install).toBeEnabled();

    await user.click(install);
    expect(await screen.findByText("Enable to let its agents and skills run")).toBeInTheDocument();
    expect(screen.getByText("Plugin installed", { exact: false })).toHaveTextContent("Plugin installed acme-reviews");

    const [dry, real] = importCalls(api);
    expect(dry!.url.search).toBe("?dry_run=true&filename=acme.zip");
    expect(real!.url.search).toBe("?dry_run=false&filename=acme.zip");
    for (const call of [dry!, real!]) {
      expect(call.init.method).toBe("POST");
      expect(new Headers(call.init.headers).get("content-type")).toBe("application/zip");
      expect(call.init.body).toBe(file);
    }
  });

  it("sends a .json file as application/json", async () => {
    const api = mockFetch({ "POST /plugins/import": makePreview() });
    renderWithProviders(<PluginImportCard />);
    const file = new File(['{"manifest":{}}'], "workspace.json", { type: "application/json" });

    await uploader().upload(fileInput(), file);
    await screen.findByRole("region", { name: /Review plugin/ });
    const [call] = importCalls(api);
    expect(new Headers(call!.init.headers).get("content-type")).toBe("application/json");
    expect(call!.init.body).toBe(file);
  });

  it("names the conflicts and keeps Install disabled", async () => {
    mockFetch({
      "POST /plugins/import": makePreview({ conflicts: { plugin: true, skills: ["secrets", "owasp-top-10"] } }),
    });
    renderWithProviders(<PluginImportCard />);

    await uploader().upload(fileInput(), zip());
    const review = await screen.findByRole("region", { name: /Review plugin/ });
    const alert = within(review).getByRole("alert");
    expect(alert).toHaveTextContent("A plugin named acme-reviews is already installed.");
    expect(alert).toHaveTextContent("These skill names already exist: secrets, owasp-top-10");
    expect(within(review).getByRole("button", { name: "Install" })).toBeDisabled();
  });

  it("shows a 409 on install as conflicts, disables Install and installs nothing", async () => {
    const api = mockFetch({
      "POST /plugins/import": (req) =>
        req.search.includes("dry_run=true")
          ? makePreview()
          : jsonResponse(
              { error: { code: "plugin_conflict", message: "Plugin conflicts", details: { plugin: false, skills: ["secrets"] } } },
              409,
            ),
    });
    const { user } = renderWithProviders(<PluginImportCard />);

    await uploader().upload(fileInput(), zip());
    await user.click(await screen.findByRole("button", { name: "Install" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("These skill names already exist: secrets");
    expect(screen.getByRole("button", { name: "Install" })).toBeDisabled();
    expect(screen.queryByText("Enable to let its agents and skills run")).not.toBeInTheDocument();
    expect(importCalls(api)).toHaveLength(2);
  });

  it("shows the message and the first issues of a 422, and the drop zone stays", async () => {
    mockFetch({
      "POST /plugins/import": () =>
        jsonResponse(
          {
            error: {
              code: "invalid_plugin",
              message: "The plugin file is not valid",
              details: { issues: ["agents[0].name is empty", "skills[1].body is too long"] },
            },
          },
          422,
        ),
    });
    renderWithProviders(<PluginImportCard />);

    await uploader().upload(fileInput(), zip());
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The plugin file is not valid");
    expect(within(alert).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "agents[0].name is empty",
      "skills[1].body is too long",
    ]);
    expect(screen.getByRole("button", { name: "browse files…" })).toBeInTheDocument();
  });

  it.each([
    [413, "payload_too_large", "Request body is too large"],
    [415, "unsupported_media_type", "Unsupported Media Type"],
  ])("shows the server message of a %i and keeps the zone", async (status, code, message) => {
    mockFetch({ "POST /plugins/import": () => jsonResponse({ error: { code, message } }, status) });
    renderWithProviders(<PluginImportCard />);

    await uploader().upload(fileInput(), zip());
    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getByRole("button", { name: "browse files…" })).toBeInTheDocument();
  });

  it("says 'Import failed — try again' for a 5xx on preview and on install, and for a network error", async () => {
    let installFails = false;
    let network = false;
    const api = mockFetch({
      "POST /plugins/import": (req) => {
        if (network) throw new TypeError("network down");
        if (req.search.includes("dry_run=true")) {
          return installFails ? makePreview() : jsonResponse({ error: { code: "internal_error", message: "db exploded" } }, 500);
        }
        return jsonResponse({ error: { code: "internal_error", message: "db exploded" } }, 500);
      },
    });
    const { user } = renderWithProviders(<PluginImportCard />);
    const up = uploader();

    // 5xx on preview: generic text, the server text is not shown, nothing installed.
    await up.upload(fileInput(), zip());
    expect(await screen.findByRole("alert")).toHaveTextContent("Import failed — try again");
    expect(screen.queryByText(/db exploded/)).not.toBeInTheDocument();

    // 5xx on install: the preview stays, Install is usable again.
    installFails = true;
    await up.upload(fileInput(), zip());
    await user.click(await screen.findByRole("button", { name: "Install" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Import failed — try again");
    expect(screen.getByRole("button", { name: "Install" })).toBeEnabled();
    expect(screen.queryByText("Enable to let its agents and skills run")).not.toBeInTheDocument();

    // Network failure: same line.
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    network = true;
    await up.upload(fileInput(), zip());
    expect(await screen.findByRole("alert")).toHaveTextContent("Import failed — try again");
    expect(api.fetch).toHaveBeenCalledTimes(4);
  });

  it("closes the preview with Escape and gives focus back to Browse files…", async () => {
    mockFetch({ "POST /plugins/import": makePreview() });
    const { user } = renderWithProviders(<PluginImportCard />);

    await uploader().upload(fileInput(), zip());
    await screen.findByRole("region", { name: /Review plugin/ });
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.getByRole("button", { name: "browse files…" })).toHaveFocus());
    expect(screen.queryByRole("region", { name: /Review plugin/ })).not.toBeInTheDocument();
  });

  it("opens the file chooser from the focusable Browse files… button", async () => {
    mockFetch({});
    const { user } = renderWithProviders(<PluginImportCard />);
    const browse = screen.getByRole("button", { name: "browse files…" });
    const input = fileInput() as HTMLInputElement;
    let clicks = 0;
    input.addEventListener("click", () => clicks++);

    await user.tab(); // the card has a single tab stop: Browse files…
    expect(browse).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(clicks).toBe(1);
    expect(input).toHaveAttribute("accept", ".zip,.json,application/zip,application/json");
  });
});

describe("PluginImportCard messages (NFR9)", () => {
  it("takes every visible string from the message files in the zone, problem, preview and installed states", async () => {
    mockFetch({
      "POST /plugins/import": (req) =>
        req.search.includes("dry_run=true")
          ? makePreview({ conflicts: { plugin: true, skills: ["secrets"] } })
          : jsonResponse(RESULT, 201),
    });
    const { user } = renderWithProviders(<PluginImportCard />, { messages: pseudoMessages() });
    const up = uploader();
    const input = () => fileInput("⟦integrationsImport.zone.fileInputAria⟧");
    // Data from the file or the API, shown next to markers: names, descriptions, ignored parts, warnings.
    const data = [
      "acme-reviews",
      "Acme review pack",
      "Security reviewer",
      "Style reviewer",
      "owasp-top-10",
      "secrets",
      "README.md",
      "Not part of the plugin",
      /^Agent Style reviewer uses model/,
    ];

    expect(strayText(document.body)).toEqual([]);
    await up.upload(input(), new File(["x"], "a.tar"));
    await screen.findByRole("alert");
    expect(strayText(document.body)).toEqual([]);

    await up.upload(input(), zip());
    await screen.findByRole("region", { name: /integrationsImport.preview.title/ });
    expect(strayText(document.body, data)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "⟦integrationsImport.preview.cancel⟧" }));

    // Without a conflict: install and the installed state.
    cleanup();
    mockFetch({ "POST /plugins/import": (req) => (req.search.includes("dry_run=true") ? makePreview() : jsonResponse(RESULT, 201)) });
    const second = renderWithProviders(<PluginImportCard />, { messages: pseudoMessages() });
    await up.upload(input(), zip());
    await second.user.click(await screen.findByRole("button", { name: "⟦integrationsImport.preview.install⟧" }));
    await screen.findByRole("button", { name: "⟦integrationsImport.installed.another⟧" });
    expect(strayText(document.body, data)).toEqual([]);
  });
});
