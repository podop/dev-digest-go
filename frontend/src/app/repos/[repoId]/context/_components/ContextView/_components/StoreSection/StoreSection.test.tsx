/* StoreSection — upload of several files (valid, skipped, suffixed), the row menu's
   delete confirmation naming "Used by N agents", and the inline rename checks
   (invalid path → no request; path_exists → inline error), over a stubbed API. */
import { describe, it, expect, afterEach, beforeAll, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import type { ContextDoc, ContextDocPreview } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { StoreSection } from "./StoreSection";

afterEach(cleanup);

// jsdom's Blob has no arrayBuffer(); browsers do.
beforeAll(() => {
  if (!Blob.prototype.arrayBuffer) {
    Blob.prototype.arrayBuffer = function arrayBuffer(this: Blob) {
      return new Promise<ArrayBuffer>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result as ArrayBuffer);
        reader.onerror = () => reject(reader.error);
        reader.readAsArrayBuffer(this);
      });
    };
  }
});

const ROOT = ".devdigest/specs/";

const storeDoc = (path: string, used_by = 0): ContextDoc => ({
  path,
  name: path.slice(path.lastIndexOf("/") + 1),
  doc_type: "specs",
  size_bytes: 10,
  tokens: 3,
  updated_at: "2026-10-01T00:00:00.000Z",
  used_by,
  source: "store",
  editable: true,
  version: 4,
});

const created = (path: string): ContextDocPreview => ({
  path,
  name: path.slice(path.lastIndexOf("/") + 1),
  doc_type: "specs",
  content: "",
  tokens: 0,
  size_bytes: 0,
  used_by: 0,
  used_by_agents: [],
  source: "store",
  editable: true,
  version: 1,
});

function setup(docs: ContextDoc[], onGo = vi.fn(), onRenamed = vi.fn()) {
  const api = mockFetch({ "GET /repos/r1/context": { clone_status: "ready", globs: [], docs, tokens_total: 0 } });
  const view = renderWithProviders(
    <StoreSection repoId="r1" docs={docs} empty={docs.length === 0} selected={docs[0]?.path ?? null} onGo={onGo} onRenamed={onRenamed} />,
  );
  return { api, onGo, onRenamed, ...view };
}

describe("StoreSection", () => {
  it("uploads the .md files one by one, skips the others with a toast naming them, and suffixes a taken name", async () => {
    const { api } = setup([]);
    // The server suffixes a taken name (on_conflict: "suffix"): the second answer is a-2.md.
    api.on("POST /repos/r1/context/files", () =>
      created(api.requests("POST", "/repos/r1/context/files").length > 1 ? `${ROOT}a-2.md` : `${ROOT}a.md`),
    );
    // A real picker can still hand over non-.md files ("All files"): do not filter by `accept`.
    const user = userEvent.setup({ applyAccept: false });
    const input = screen.getByLabelText("Markdown files to upload");
    const big = new File(["x".repeat(300 * 1024)], "c.md", { type: "text/markdown" });
    await user.upload(input, [new File(["# A"], "a.md"), new File(["plain"], "b.txt"), big]);

    expect(await screen.findByText("b.txt skipped: only .md files can be uploaded.")).toBeInTheDocument();
    expect(screen.getByText("c.md skipped: larger than 256 KB.")).toBeInTheDocument();
    expect(await screen.findByText("1 file uploaded")).toBeInTheDocument();
    const posts = api.requests("POST", "/repos/r1/context/files");
    expect(posts).toHaveLength(1);
    expect(posts[0]?.body).toEqual({ path: `${ROOT}a.md`, content: "# A", on_conflict: "suffix" });

    await user.upload(input, new File(["# A"], "a.md"));
    await vi.waitFor(() => expect(screen.getAllByText("1 file uploaded")).toHaveLength(2));
    expect(api.requests("POST", "/repos/r1/context/files")[1]?.body).toMatchObject({ path: `${ROOT}a.md`, on_conflict: "suffix" });
  });

  it("delete asks for confirmation naming 'Used by 2 agents', then sends DELETE and selects nothing", async () => {
    const files = [storeDoc(`${ROOT}rules.md`, 2)];
    const { api, onGo, user } = setup(files);
    api.on("DELETE /repos/r1/context/files", jsonResponse(null, 204));

    await user.click(screen.getByRole("button", { name: "Actions for rules.md" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/Used by 2 agents/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));

    await vi.waitFor(() => expect(api.requests("DELETE", "/repos/r1/context/files")).toHaveLength(1));
    expect(api.requests("DELETE", "/repos/r1/context/files")[0]?.search).toBe(`?path=${encodeURIComponent(`${ROOT}rules.md`)}`);
    await vi.waitFor(() => expect(onGo).toHaveBeenCalledWith(null));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("rename: an invalid name shows an inline error without a request; a taken path shows the server's answer inline", async () => {
    const files = [storeDoc(`${ROOT}rules.md`)];
    const { api, onGo, onRenamed, user } = setup(files);
    api.on("POST /repos/r1/context/files/rename", jsonResponse({ error: { code: "path_exists", message: "taken" } }, 409));

    await user.click(screen.getByRole("button", { name: "Actions for rules.md" }));
    await user.click(screen.getByRole("button", { name: "Rename" }));
    const field = screen.getByRole("textbox", { name: "New path for rules.md" });
    await user.clear(field);
    await user.type(field, "bad name.md{Enter}");
    expect(screen.getByRole("alert")).toHaveTextContent("Use letters, digits");
    expect(api.requests("POST", "/repos/r1/context/files/rename")).toHaveLength(0);

    await user.clear(field);
    await user.type(field, "taken.md{Enter}");
    expect(await screen.findByText("A file with this path already exists.")).toBeInTheDocument();
    expect(api.requests("POST", "/repos/r1/context/files/rename")[0]?.body).toEqual({
      path: `${ROOT}rules.md`,
      new_path: `${ROOT}taken.md`,
      base_version: 4,
    });
    expect(onGo).not.toHaveBeenCalled();
    expect(onRenamed).not.toHaveBeenCalled();

    // A free path: the owner is told, so it can move the selection and the draft.
    api.on("POST /repos/r1/context/files/rename", created(`${ROOT}ok.md`));
    await user.clear(field);
    await user.type(field, "ok.md{Enter}");
    await vi.waitFor(() => expect(onRenamed).toHaveBeenCalledWith(`${ROOT}rules.md`, `${ROOT}ok.md`));
  });
});
