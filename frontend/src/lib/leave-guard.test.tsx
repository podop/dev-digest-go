import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup, userEvent } from "@/test/render";
import { LeaveGuardProvider, useConfirmLeave, useLeaveGuard } from "./leave-guard";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function Screen({ dirty, onLeave }: { dirty: boolean; onLeave: (allowed: boolean) => void }) {
  useLeaveGuard(dirty, "Discard unsaved changes?");
  const confirmLeave = useConfirmLeave();
  return (
    <>
      <button onClick={() => onLeave(confirmLeave())}>shell navigate</button>
      <a href="/elsewhere" onClick={(e) => e.preventDefault()}>
        go elsewhere
      </a>
    </>
  );
}

function setup(dirty: boolean, onLeave = vi.fn()) {
  const ui = (d: boolean) => (
    <LeaveGuardProvider>
      <Screen dirty={d} onLeave={onLeave} />
    </LeaveGuardProvider>
  );
  const view = render(ui(dirty));
  return { onLeave, setDirty: (d: boolean) => view.rerender(ui(d)) };
}

describe("leave guard", () => {
  it("blocks shell navigation and link clicks while dirty unless the user confirms", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { onLeave } = setup(true);

    await user.click(screen.getByRole("button", { name: "shell navigate" }));
    expect(confirm).toHaveBeenCalledWith("Discard unsaved changes?");
    expect(onLeave).toHaveBeenLastCalledWith(false);

    const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    screen.getByRole("link", { name: "go elsewhere" }).dispatchEvent(click);
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(click.defaultPrevented).toBe(true);

    confirm.mockReturnValue(true);
    await user.click(screen.getByRole("button", { name: "shell navigate" }));
    expect(onLeave).toHaveBeenLastCalledWith(true);
  });

  it("passes without asking when clean, after the draft is saved, and without a provider", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { onLeave, setDirty } = setup(false);

    await user.click(screen.getByRole("button", { name: "shell navigate" }));
    expect(onLeave).toHaveBeenLastCalledWith(true);

    setDirty(true);
    setDirty(false);
    await user.click(screen.getByRole("button", { name: "shell navigate" }));
    expect(onLeave).toHaveBeenLastCalledWith(true);
    expect(confirm).not.toHaveBeenCalled();
    cleanup();

    const loose = vi.fn();
    render(<Screen dirty onLeave={loose} />);
    await user.click(screen.getByRole("button", { name: "shell navigate" }));
    expect(loose).toHaveBeenCalledWith(true);
  });
});
