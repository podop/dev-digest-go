/* PathList — AC17: Open opens GitHub at the tour's indexed commit in a new tab; AC22: a 200-char
   path is truncated (ellipsis + tooltip). Reading path is numbered; empty → "Not enough information". */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { PathList } from "./PathList";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const ITEMS = [
  { path: "src/server.ts", reason: "App bootstrap" },
  { path: "src/lib/redis.ts", reason: "Shared Redis singleton" },
];

describe("PathList", () => {
  it("Open opens the file on GitHub at the indexed commit in a new tab", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const { user } = renderWithProviders(
      <PathList items={ITEMS} variant="critical" repoFullName="acme/payments-api" indexedSha="abc123" />,
    );

    await user.click(screen.getByRole("button", { name: "Open src/lib/redis.ts on GitHub" }));

    expect(open).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith(
      "https://github.com/acme/payments-api/blob/abc123/src/lib/redis.ts",
      "_blank",
      "noopener",
    );
  });

  it("truncates a very long path with the full path as tooltip", () => {
    const long = `${"deep/".repeat(39)}file.ts`;
    renderWithProviders(
      <PathList items={[{ path: long, reason: "r" }]} variant="critical" repoFullName="a/b" indexedSha="s" />,
    );

    const el = screen.getByTitle(long);
    expect(el).toHaveTextContent(long);
    expect(el).toHaveStyle({ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" });
  });

  it("reading path is a numbered list with no Open button", () => {
    renderWithProviders(<PathList items={ITEMS} variant="reading" repoFullName="a/b" indexedSha="s" />);

    const list = screen.getByRole("list", { name: "Guided reading path" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByText("1")).toBeInTheDocument();
    expect(within(list).getByText("Shared Redis singleton")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows the empty state", () => {
    renderWithProviders(<PathList items={[]} variant="critical" repoFullName="a/b" indexedSha="s" />);
    expect(screen.getByText("Not enough information")).toBeInTheDocument();
  });
});
