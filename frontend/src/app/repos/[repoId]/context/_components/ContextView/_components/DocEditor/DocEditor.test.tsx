/* DocEditor — the store-file editor over a draft the parent owns (here a tiny host):
   an unsaved indicator, Save and Ctrl/Cmd+S send PUT with the base version and show
   "Saved" for 2 s; a save whose file was deleted elsewhere shows the banner (Reload /
   Keep editing) and keeps the typed text; Keep editing makes the next Save re-create it. The stale_version flow runs through ContextView.test.tsx. */
import React from "react";
import { describe, it, expect, afterEach } from "vitest";
import type { ContextDocPreview } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, fireEvent, waitFor } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { DocEditor } from "./DocEditor";

const PATH = ".devdigest/specs/rules.md";

const DOC: ContextDocPreview = {
  path: PATH,
  name: "rules.md",
  doc_type: "specs",
  content: "# Rules\n",
  tokens: 2,
  size_bytes: 8,
  used_by: 0,
  used_by_agents: [],
  source: "store",
  editable: true,
  version: 3,
};

/** Owns what the ContextView owns: the draft, and the saved doc the editor compares it with. */
function Host() {
  const [doc, setDoc] = React.useState(DOC);
  const [text, setText] = React.useState(DOC.content);
  return (
    <DocEditor
      repoId="r1"
      doc={doc}
      text={text}
      onChange={setText}
      onSaved={(_path, saved) => setDoc((d) => ({ ...d, content: saved, version: (d.version ?? 0) + 1 }))}
      onDiscard={() => setText(doc.content)}
    />
  );
}

const field = () => screen.findByRole("textbox", { name: "Document content" }, { timeout: 5000 });

afterEach(() => {
  cleanup();
});

describe("DocEditor", () => {
  it("shows an unsaved indicator, saves with the base version via the button and Ctrl+S, and flashes Saved for 2 s", async () => {
    const api = mockFetch({
      "PUT /repos/r1/context/files": (req) => ({ ...DOC, content: (req.body as { content: string }).content }),
    });
    const { user } = renderWithProviders(<Host />);
    const text = await field();
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();

    await user.type(text, "more");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    await user.click(save);

    expect(await screen.findByText("Saved")).toBeInTheDocument();
    const [first] = api.requests("PUT", "/repos/r1/context/files");
    expect(first?.search).toBe(`?path=${encodeURIComponent(PATH)}`);
    expect(first?.body).toEqual({ content: "# Rules\nmore", base_version: 3 });
    await waitFor(() => expect(screen.queryByText("Saved")).not.toBeInTheDocument(), { timeout: 3500 });

    // Ctrl+S saves too (the browser's save dialog is suppressed) and sends the bumped version.
    await user.type(text, "!");
    expect(fireEvent.keyDown(text, { key: "s", ctrlKey: true })).toBe(false);
    await screen.findByText("Saved");
    expect(api.requests("PUT", "/repos/r1/context/files")[1]?.body).toEqual({ content: "# Rules\nmore!", base_version: 4 });
  });

  it("a save of a file deleted elsewhere offers Reload and Keep editing; Reload drops the draft", async () => {
    const api = mockFetch({
      "PUT /repos/r1/context/files": jsonResponse({ error: { code: "doc_not_found", message: "gone" } }, 404),
    });
    const { user } = renderWithProviders(<Host />);
    const text = await field();
    await user.type(text, "keep me");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("This file was deleted or renamed elsewhere");
    expect(text).toHaveValue("# Rules\nkeep me");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Keep editing" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(text).toHaveValue("# Rules\n");
    expect(api.requests("POST", "/repos/r1/context/files")).toHaveLength(0);
  });

  it("Keep editing after a deleted file keeps the text and turns Save into a re-create (POST, on_conflict fail)", async () => {
    const api = mockFetch({
      "PUT /repos/r1/context/files": jsonResponse({ error: { code: "doc_not_found", message: "gone" } }, 404),
      "POST /repos/r1/context/files": (req) => ({ ...DOC, content: (req.body as { content: string }).content, version: 1 }),
    });
    const { user } = renderWithProviders(<Host />);
    const text = await field();
    await user.type(text, "keep me");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");

    await user.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(text).toHaveValue("# Rules\nkeep me");

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(api.requests("PUT", "/repos/r1/context/files")).toHaveLength(1);
    const [created] = api.requests("POST", "/repos/r1/context/files");
    expect(created?.body).toEqual({ path: PATH, content: "# Rules\nkeep me", on_conflict: "fail" });
    expect(text).toHaveValue("# Rules\nkeep me");
  });

  it("a re-create onto a path taken again shows the banner, keeps the text and Keep editing goes back to overwriting", async () => {
    const api = mockFetch({
      "PUT /repos/r1/context/files": jsonResponse({ error: { code: "doc_not_found", message: "gone" } }, 404),
      "POST /repos/r1/context/files": jsonResponse({ error: { code: "path_exists", message: "exists" } }, 409),
    });
    const { user } = renderWithProviders(<Host />);
    const text = await field();
    await user.type(text, "keep me");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await user.click(await screen.findByRole("button", { name: "Keep editing" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("A file already exists at this path");
    expect(text).toHaveValue("# Rules\nkeep me");
    expect(api.requests("POST", "/repos/r1/context/files")).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Keep editing" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.requests("PUT", "/repos/r1/context/files")).toHaveLength(2));
    expect(api.requests("POST", "/repos/r1/context/files")).toHaveLength(1);
    expect(text).toHaveValue("# Rules\nkeep me");
  });
});
