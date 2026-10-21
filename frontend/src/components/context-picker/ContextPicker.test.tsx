/* ContextPicker — Preview opens the rendered document over the tab and closing
   keeps the tab state (filter, attachments); an attached path the repo no
   longer lists is a "missing" row that can be unchecked; long paths are cut with
   the full path in the tooltip; filtering turns dragging off; icon preview for
   the skill tab; empty and not-cloned states. Real hooks over a stubbed API. */
import React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, within } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { makeDoc, makeList, makePreview } from "@/test/context-fixtures";
import { ContextPicker } from "./ContextPicker";

const LONG = `docs/${"very-long-folder-name/".repeat(8)}${"x".repeat(60)}.md`;

function Harness({ initial, previewAs = "button" }: { initial: string[]; previewAs?: "button" | "icon" }) {
  const [attached, setAttached] = React.useState(initial);
  return (
    <ContextPicker
      repoId="r1"
      attached={attached}
      onChange={setAttached}
      title="Picker"
      badge={({ attached: k, total }) => `${k}/${total}`}
      hint="Hint text"
      previewAs={previewAs}
    >
      <p>footer: {attached.join(",") || "none"}</p>
    </ContextPicker>
  );
}

let api: ReturnType<typeof mockFetch>;
beforeEach(() => {
  api = mockFetch({
    "GET /repos/r1/context": makeList(),
    "GET /repos/r1/context/doc": (req) =>
      makePreview(new URLSearchParams(req.search).get("path") ?? "", "# Rules heading\n\nBe strict.\n\n<script>alert(1)</script>\n"),
  });
});
afterEach(cleanup);

describe("ContextPicker", () => {
  it("Preview opens the rendered markdown over the tab; closing keeps the filter and the attachments (AC24)", async () => {
    const { user } = renderWithProviders(<Harness initial={["specs/public-api.md"]} />);
    expect(await screen.findByText("1/7")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Filter documents" }), "spec");
    await user.click(screen.getByRole("button", { name: "Preview rate-limiting.md" }));

    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByRole("heading", { name: "Rules heading" })).toBeInTheDocument();
    // Raw HTML in a document is text, never an element.
    expect(within(dialog).getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
    expect(dialog.querySelector("script")).toBeNull();
    expect(api.requests("GET", "/repos/r1/context/doc")[0]?.search).toContain(encodeURIComponent("specs/rate-limiting.md"));

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Filter documents" })).toHaveValue("spec");
    expect(screen.getByText("footer: specs/public-api.md")).toBeInTheDocument();
  });

  it("shows an attached path the repo no longer lists as a 'missing' row that can be unchecked; long paths are cut with a tooltip (AC26)", async () => {
    api.on("GET /repos/r1/context", makeList({ docs: [makeDoc(LONG), makeDoc("docs/a.md")] }));
    const { user } = renderWithProviders(<Harness initial={["gone/old.md", LONG]} />);
    expect(await screen.findByText("2/2")).toBeInTheDocument();

    const rows = screen.getAllByTestId("context-doc-row");
    expect(rows.map((r) => r.dataset.path)).toEqual(["gone/old.md", LONG, "docs/a.md"]);
    expect(within(rows[0]!).getByText("missing")).toBeInTheDocument();
    expect(within(rows[0]!).queryByRole("button", { name: /Preview/ })).not.toBeInTheDocument();
    // Truncation is CSS (ellipsis); the full path is the tooltip.
    expect(within(rows[1]!).getByText(/^x{60}\.md$/)).toHaveAttribute("title", LONG);
    expect(within(rows[1]!).getByText(/^x{60}\.md$/)).toHaveStyle({ textOverflow: "ellipsis" });

    await user.click(within(rows[0]!).getByRole("checkbox"));
    expect(await screen.findByText(`footer: ${LONG}`)).toBeInTheDocument();
  });

  it("filtering turns dragging off; the skill variant previews with an icon button", async () => {
    const { user } = renderWithProviders(<Harness initial={["specs/public-api.md"]} previewAs="icon" />);
    await screen.findByText("1/7");
    expect(screen.getByRole("button", { name: "Drag to reorder specs/public-api.md" })).toBeEnabled();
    expect(screen.queryByText("Preview")).not.toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Filter documents" }), "api");
    expect(screen.getByRole("button", { name: "Drag to reorder specs/public-api.md" })).toBeDisabled();
    expect(screen.getAllByTestId("context-doc-row")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Preview public-api.md" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("explains an empty repo and a repo that is not cloned", async () => {
    api.on("GET /repos/r1/context", makeList({ docs: [] }));
    const first = renderWithProviders(<Harness initial={[]} />);
    expect(await screen.findByText(/No documents match/)).toBeInTheDocument();
    first.unmount();

    api.on("GET /repos/r1/context", { clone_status: "not_cloned", globs: [], docs: [], tokens_total: 0 });
    renderWithProviders(<Harness initial={[]} />);
    expect(await screen.findByText("Repository is not cloned yet")).toBeInTheDocument();
  });
});
