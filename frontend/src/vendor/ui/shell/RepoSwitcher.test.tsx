/* RepoSwitcher — local exception to the vendored shell (user 2026-10-27): "Add repository…" (and its
   divider) is shown only when the host passes `onAddRepo`. */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RepoSwitcher } from "./RepoSwitcher";
import type { ShellContext } from "./types";

afterEach(cleanup);

const REPOS = [{ id: "r1", full_name: "acme/api" }];
const ctx = (over: Partial<ShellContext> = {}) =>
  ({ repos: REPOS, activeRepo: null, onSelectRepo: vi.fn(), ...over }) as unknown as ShellContext;

describe("RepoSwitcher", () => {
  it("lists the repositories and no 'Add repository…' item without onAddRepo", async () => {
    const user = userEvent.setup();
    render(<RepoSwitcher ctx={ctx()} />);
    await user.click(screen.getByText("No repo selected"));
    expect(screen.getByRole("button", { name: "acme/api" })).toBeInTheDocument();
    expect(screen.queryByText("Add repository…")).toBeNull();
  });

  it("shows 'Add repository…' and calls onAddRepo when the host provides it", async () => {
    const user = userEvent.setup();
    const onAddRepo = vi.fn();
    render(<RepoSwitcher ctx={ctx({ onAddRepo })} />);
    await user.click(screen.getByText("No repo selected"));
    await user.click(within(document.body).getByRole("button", { name: "Add repository…" }));
    expect(onAddRepo).toHaveBeenCalledTimes(1);
  });
});
