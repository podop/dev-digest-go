/* ContextView — document list (names, folders, types, summary), the store section
   (header, toolbar, store files above repo documents, New file / New folder), filter,
   row selection via ?doc=, markdown preview with "Used by N agents", and the
   empty / not-cloned / error states, with the real hooks over a stubbed API.
   the Preview | Edit editor over a store file (unsaved-switch confirmation, stale save,
   read-only repo documents). AppShell is a passthrough. */
import React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { ContextDocPreview, ContextList } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { RepoProvider } from "@/lib/repo-context";

const replace = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace }),
  usePathname: () => "/repos/r1/context",
}));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { ContextView } from "./ContextView";

const REPO = {
  id: "r1",
  workspace_id: "w1",
  owner: "acme",
  name: "payments-api",
  full_name: "acme/payments-api",
  default_branch: "main",
  clone_path: "/clones/acme/payments-api",
  last_polled_at: null,
  created_by: null,
};

const LIST: ContextList = {
  clone_status: "ready",
  globs: ["**/{specs,docs,insights}/**/*.md"],
  tokens_total: 1234,
  docs: [
    { path: "docs/architecture.md", name: "architecture.md", doc_type: "docs", size_bytes: 400, tokens: 100, updated_at: "2026-10-01T00:00:00.000Z", used_by: 2, source: "repo", editable: false },
    { path: "insights/notes/db.md", name: "db.md", doc_type: "insights", size_bytes: 800, tokens: 200, updated_at: "2026-10-01T00:00:00.000Z", used_by: 0, source: "repo", editable: false },
    { path: "specs/public-api.md", name: "public-api.md", doc_type: "specs", size_bytes: 3736, tokens: 934, updated_at: "2026-10-01T00:00:00.000Z", used_by: 3, source: "repo", editable: false },
  ],
};

const PREVIEW: ContextDocPreview = {
  path: "specs/public-api.md",
  name: "public-api.md",
  doc_type: "specs",
  content: "# Public API — PRD\n\nAll endpoints MUST be rate-limited.\n\n<script>alert(1)</script>\n",
  tokens: 934,
  size_bytes: 3736,
  used_by: 3,
  source: "repo",
  editable: false,
  used_by_agents: [
    { id: "ag1", name: "Security Reviewer", via: "direct" },
    { id: "ag2", name: "General Reviewer", via: "skill", skill_name: "api-rules" },
  ],
};

function setup(list: ContextList | Response = LIST, preview: ContextDocPreview | Response = PREVIEW) {
  return mockFetch({
    "GET /repos": [REPO],
    "GET /repos/r1/context": list,
    "GET /repos/r1/context/doc": preview,
  });
}

beforeEach(() => {
  replace.mockReset();
});
afterEach(cleanup);

describe("ContextView", () => {
  it("lists the documents with folder, type and the totals; filters; selecting a row updates ?doc=", async () => {
    setup();
    const { user } = renderWithProviders(<ContextView repoId="r1" doc={null} />);

    const list = await screen.findByRole("list", { name: "Documents" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(within(list).getByText("db.md")).toBeInTheDocument();
    expect(within(list).getByText("insights/notes/")).toBeInTheDocument();
    expect(within(list).getByText("specs")).toBeInTheDocument();
    expect(screen.getByText("3 docs · ≈ 1,234 tokens total")).toBeInTheDocument();
    expect(screen.getByText("Select a document")).toBeInTheDocument();

    await user.type(screen.getByRole("textbox", { name: "Filter documents" }), "api");
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    await user.clear(screen.getByRole("textbox", { name: "Filter documents" }));
    await user.type(screen.getByRole("textbox", { name: "Filter documents" }), "zzz");
    expect(screen.getByText("No documents match your filter.")).toBeInTheDocument();
    await user.clear(screen.getByRole("textbox", { name: "Filter documents" }));

    await user.click(screen.getByRole("button", { name: /public-api\.md/ }));
    expect(replace).toHaveBeenCalledWith("/repos/r1/context?doc=specs%2Fpublic-api.md", { scroll: false });
  });

  it("renders the selected document as markdown, with Used by N agents; raw HTML stays text", async () => {
    const api = setup();
    renderWithProviders(<ContextView repoId="r1" doc="specs/public-api.md" />);

    expect(await screen.findByRole("heading", { name: "Public API — PRD" })).toBeInTheDocument();
    expect(screen.getByText("Used by 3 agents")).toHaveAttribute(
      "title",
      "Security Reviewer (attached directly)\nGeneral Reviewer (via skill api-rules)",
    );
    expect(screen.getByText("≈ 934 tokens")).toBeInTheDocument();
    // A repository document is read-only: Edit is disabled with the hint, and there is no row menu (rename / delete).
    expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();
    expect(screen.getByTitle("Repository files are read-only — create a copy in .devdigest/specs/ to edit")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Actions for/ })).not.toBeInTheDocument();
    // <script> shows up as text, never as an element.
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
    expect(api.requests("GET", "/repos/r1/context/doc")[0]?.search).toBe("?path=specs%2Fpublic-api.md");
  });

  it("shows a message for a document that is gone or too large", async () => {
    setup(LIST, jsonResponse({ error: { code: "doc_too_large", message: "big" } }, 413));
    renderWithProviders(<ContextView repoId="r1" doc="specs/big.md" />);
    expect(await screen.findByText("This document is larger than 256 KB and cannot be previewed.")).toBeInTheDocument();
  });

  it("shows the empty state naming the globs and the not-cloned notice", async () => {
    setup({ ...LIST, docs: [], tokens_total: 0 });
    const first = renderWithProviders(<ContextView repoId="r1" doc={null} />);
    expect(await screen.findByText("No documents match **/{specs,docs,insights}/**/*.md")).toBeInTheDocument();
    first.unmount();

    setup({ clone_status: "not_cloned", globs: LIST.globs, docs: [], tokens_total: 0 });
    renderWithProviders(<ContextView repoId="r1" doc={null} />);
    expect(await screen.findByText("Repository is not cloned yet")).toBeInTheDocument();
  });

  it("shows the error state and retries the list", async () => {
    const api = setup(jsonResponse({ error: { code: "internal", message: "boom" } }, 500));
    const { user } = renderWithProviders(<ContextView repoId="r1" doc={null} />);

    expect(await screen.findByText("Couldn’t load documents")).toBeInTheDocument();
    api.on("GET /repos/r1/context", LIST);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("list", { name: "Documents" })).toBeInTheDocument();
  });

  it("refresh refetches the list", async () => {
    const api = setup();
    const { user } = renderWithProviders(<ContextView repoId="r1" doc={null} />);
    await screen.findByRole("list", { name: "Documents" });

    await user.click(screen.getByRole("button", { name: "Refresh documents" }));
    await vi.waitFor(() => expect(api.requests("GET", "/repos/r1/context")).toHaveLength(2));
  });

  describe("store files", () => {
    const stamp = "2026-10-01T00:00:00.000Z";
    const storeDoc = (path: string, used_by = 0): ContextList["docs"][number] => ({
      path,
      name: path.slice(path.lastIndexOf("/") + 1),
      doc_type: "specs",
      size_bytes: 10,
      tokens: 3,
      updated_at: stamp,
      used_by,
      source: "store",
      editable: true,
      version: 1,
    });

    it("shows the header, root, labelled toolbar, the empty hint and then the files above the repo documents", async () => {
      const api = setup();
      renderWithProviders(<ContextView repoId="r1" doc={null} />);

      expect(await screen.findByText("Project context")).toBeInTheDocument();
      expect(screen.getByText(".devdigest/specs/")).toBeInTheDocument();
      for (const name of ["New file", "New folder", "Upload markdown files", "Refresh documents"]) {
        expect(screen.getByRole("button", { name })).toBeInTheDocument();
      }
      expect(await screen.findByText("No files yet — create or upload a spec.")).toBeInTheDocument();
      cleanup();

      api.on("GET /repos/r1/context", {
        ...LIST,
        docs: [storeDoc(".devdigest/specs/security.md"), storeDoc(".devdigest/specs/docs-x/untitled.md"), ...LIST.docs],
      });
      renderWithProviders(<ContextView repoId="r1" doc={null} />);

      const files = await screen.findByRole("list", { name: "Store files" });
      expect(within(files).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["security.md", "docs-x/untitled.md"]);
      const repoDocs = screen.getByRole("list", { name: "Documents" });
      expect(files.compareDocumentPosition(repoDocs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      expect(screen.queryByText("No files yet — create or upload a spec.")).not.toBeInTheDocument();
    });

    it("New file creates untitled.md, selects it in Edit mode and opens its name for rename; New folder creates <folder>/untitled.md", async () => {
      let docs: ContextList["docs"] = [...LIST.docs];
      const api = setup();
      api.on("GET /repos/r1/context", () => ({ ...LIST, docs }));
      api.on("POST /repos/r1/context/files", (req) => {
        const path = (req.body as { path?: string }).path ?? ".devdigest/specs/untitled.md";
        docs = [storeDoc(path), ...docs];
        return { ...PREVIEW, path, name: "untitled.md", source: "store", editable: true, version: 1 };
      });
      const { user } = renderWithProviders(<ContextView repoId="r1" doc={null} />);
      await screen.findByRole("list", { name: "Documents" });

      await user.click(screen.getByRole("button", { name: "New file" }));
      expect(await screen.findByRole("textbox", { name: "New path for untitled.md" })).toHaveValue("untitled.md");
      expect(api.requests("POST", "/repos/r1/context/files")[0]?.body).toEqual({});
      expect(replace).toHaveBeenLastCalledWith("/repos/r1/context?doc=.devdigest%2Fspecs%2Funtitled.md&mode=edit", { scroll: false });

      await user.click(screen.getByRole("button", { name: "New folder" }));
      await user.type(screen.getByRole("textbox", { name: "Folder name" }), "docs x");
      expect(screen.getByRole("alert")).toHaveTextContent("Use letters, digits");
      await user.clear(screen.getByRole("textbox", { name: "Folder name" }));
      await user.type(screen.getByRole("textbox", { name: "Folder name" }), "docs-x");
      await user.click(screen.getByRole("button", { name: "Create" }));

      await vi.waitFor(() => expect(api.requests("POST", "/repos/r1/context/files")).toHaveLength(2));
      expect(api.requests("POST", "/repos/r1/context/files")[1]?.body).toEqual({
        path: ".devdigest/specs/docs-x/untitled.md",
        on_conflict: "suffix",
      });
      await vi.waitFor(() =>
        expect(replace).toHaveBeenLastCalledWith("/repos/r1/context?doc=.devdigest%2Fspecs%2Fdocs-x%2Funtitled.md&mode=edit", { scroll: false }),
      );
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  describe("editor", () => {
    const A = ".devdigest/specs/a.md";
    const B = ".devdigest/specs/b.md";
    const stamp = "2026-10-01T00:00:00.000Z";
    const listed = (path: string): ContextList["docs"][number] => ({
      path,
      name: path.slice(path.lastIndexOf("/") + 1),
      doc_type: "specs",
      size_bytes: 10,
      tokens: 3,
      updated_at: stamp,
      used_by: 0,
      source: "store",
      editable: true,
      version: 1,
    });

    /** The server's copy of the two store files; a PUT with an old base_version is refused. */
    function setupStore() {
      const files: Record<string, { content: string; version: number }> = {
        [A]: { content: "# A\n", version: 2 },
        [B]: { content: "# B\n", version: 1 },
      };
      const preview = (path: string): ContextDocPreview => ({
        ...PREVIEW,
        path,
        name: path.slice(path.lastIndexOf("/") + 1),
        content: files[path]!.content,
        version: files[path]!.version,
        source: "store",
        editable: true,
      });
      const api = setup({ ...LIST, docs: [listed(A), listed(B), ...LIST.docs] });
      api.on("GET /repos/r1/context/doc", (req) => preview(new URLSearchParams(req.search).get("path") ?? ""));
      api.on("PUT /repos/r1/context/files", (req) => {
        const path = new URLSearchParams(req.search).get("path") ?? "";
        const body = req.body as { content: string; base_version: number };
        const file = files[path]!;
        if (body.base_version !== file.version) {
          return jsonResponse({ error: { code: "stale_version", message: "stale" } }, 409);
        }
        files[path] = { content: body.content, version: file.version + 1 };
        return preview(path);
      });
      return { api, files };
    }

    /** ContextView with the URL state router.replace would produce. */
    function Host({ initial }: { initial: string }) {
      const [sel, setSel] = React.useState<{ doc: string | null; mode: "preview" | "edit" }>({ doc: initial, mode: "edit" });
      replace.mockImplementation((href: string) => {
        const url = new URL(href, "http://localhost");
        setSel({ doc: url.searchParams.get("doc"), mode: url.searchParams.get("mode") === "edit" ? "edit" : "preview" });
      });
      return <ContextView repoId="r1" doc={sel.doc} mode={sel.mode} />;
    }

    const field = () => screen.findByRole("textbox", { name: "Document content" }, { timeout: 5000 });

    it("switching to another file with unsaved changes asks first; Cancel keeps the editor and the text, OK opens the other file", async () => {
      setupStore();
      const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
      const { user } = renderWithProviders(<Host initial={A} />);
      const text = await field();
      expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();

      await user.type(text, "draft");
      expect(screen.getByRole("img", { name: "Unsaved changes" })).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "b.md" }));
      expect(confirm).toHaveBeenCalledWith("You have unsaved changes to this file. Leave anyway?");
      expect(text).toHaveValue("# A\ndraft");

      confirm.mockReturnValue(true);
      await user.click(screen.getByRole("button", { name: "b.md" }));
      expect(await screen.findByRole("heading", { name: "B" })).toBeInTheDocument();
      expect(screen.queryByRole("img", { name: "Unsaved changes" })).not.toBeInTheDocument();
      confirm.mockRestore();
    });

    it("a stale save shows the banner with the text kept; Keep editing then overwrites, Reload loads the server version", async () => {
      const { api, files } = setupStore();
      const { user } = renderWithProviders(<Host initial={A} />);
      const text = await field();

      files[A] = { content: "# A by someone else\n", version: 3 };
      await user.type(text, "mine");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("changed since you opened it");
      expect(text).toHaveValue("# A\nmine");
      expect(screen.queryByText("Saved")).not.toBeInTheDocument();

      // Keep editing: the live version is adopted, so the next Save overwrites it.
      await user.click(screen.getByRole("button", { name: "Keep editing" }));
      await vi.waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
      expect(text).toHaveValue("# A\nmine");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(await screen.findByText("Saved")).toBeInTheDocument();
      expect(api.requests("PUT", "/repos/r1/context/files")[1]?.body).toEqual({ content: "# A\nmine", base_version: 3 });

      // Stale again, then Reload: the draft is dropped for the server's text.
      files[A] = { content: "# A v5\n", version: 5 };
      await user.type(text, "!");
      await user.click(screen.getByRole("button", { name: "Save" }));
      await user.click(await screen.findByRole("button", { name: "Reload" }));
      await vi.waitFor(() => expect(text).toHaveValue("# A v5\n"));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("a save of a file deleted elsewhere: Reload shows the deleted state; Keep editing re-creates it with the draft", async () => {
      const { api, files } = setupStore();
      const gone = () => jsonResponse({ error: { code: "doc_not_found", message: "gone" } }, 404);
      api.on("PUT /repos/r1/context/files", gone);
      const { user } = renderWithProviders(<Host initial={A} />);
      const text = await field();
      await user.type(text, "mine");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("This file was deleted or renamed elsewhere");

      // Keep editing: the text stays and Save now POSTs the draft to the same path.
      api.on("POST /repos/r1/context/files", (req) => {
        const body = req.body as { path: string; content: string };
        files[body.path] = { content: body.content, version: 1 };
        return { ...PREVIEW, path: body.path, name: "a.md", content: body.content, version: 1, source: "store", editable: true };
      });
      await user.click(screen.getByRole("button", { name: "Keep editing" }));
      expect(text).toHaveValue("# A\nmine");
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(await screen.findByText("Saved")).toBeInTheDocument();
      expect(api.requests("POST", "/repos/r1/context/files")[0]?.body).toEqual({ path: A, content: "# A\nmine", on_conflict: "fail" });

      // Deleted again, saved again, then Reload: the preview shows the deleted state.
      api.on("PUT /repos/r1/context/files", gone);
      await user.type(await field(), "!");
      await user.click(screen.getByRole("button", { name: "Save" }));
      await screen.findByRole("alert");
      api.on("GET /repos/r1/context/doc", gone);
      await user.click(screen.getByRole("button", { name: "Reload" }));
      expect(await screen.findByText("This document no longer exists in the repository.")).toBeInTheDocument();
    });
  });

  describe("under the real RepoProvider (it loads GET /repos)", () => {
    it("a known repo renders its documents once the repos list has loaded", async () => {
      setup();
      renderWithProviders(
        <RepoProvider>
          <ContextView repoId="r1" doc={null} />
        </RepoProvider>,
      );
      expect(await screen.findByRole("list", { name: "Documents" })).toBeInTheDocument();
      expect(screen.queryByText("No repo selected")).not.toBeInTheDocument();
    });

    it("a stale :repoId shows the 'No repo selected' state instead of the document list", async () => {
      const api = setup();
      api.on("GET /repos/gone/context", jsonResponse({ error: { code: "repo_not_found", message: "nope" } }, 404));
      renderWithProviders(
        <RepoProvider>
          <ContextView repoId="gone" doc={null} />
        </RepoProvider>,
      );
      expect(await screen.findByText("No repo selected")).toBeInTheDocument();
      expect(screen.queryByRole("list", { name: "Documents" })).not.toBeInTheDocument();
      expect(screen.queryByText("Couldn’t load documents")).not.toBeInTheDocument();
    });
  });
});
