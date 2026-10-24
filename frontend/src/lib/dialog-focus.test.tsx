import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { focusableIn, useDialogFocus, wrapTarget } from "./dialog-focus";

afterEach(cleanup);

function Host({ onClose }: { onClose: () => void }) {
  const ref = useDialogFocus(onClose);
  return (
    <div ref={ref}>
      <div role="dialog" aria-label="Demo">
        <button type="button">First</button>
        <button type="button" disabled>
          Skipped
        </button>
        <button type="button">Last</button>
      </div>
    </div>
  );
}

describe("useDialogFocus", () => {
  it("focuses in on open, traps Tab, closes on Escape and returns focus to the opener", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();

    const { unmount } = render(<Host onClose={onClose} />);
    const first = screen.getByRole("button", { name: "First" });
    const last = screen.getByRole("button", { name: "Last" });
    expect(first).toHaveFocus();

    await user.tab({ shift: true });
    expect(last).toHaveFocus(); // wraps backwards, skipping nothing it should not
    await user.tab();
    expect(first).toHaveFocus(); // wraps forwards

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);

    unmount();
    expect(opener).toHaveFocus();
    opener.remove();
  });
});

describe("focusableIn / wrapTarget", () => {
  it("lists enabled, visible tab stops and says where Tab goes", () => {
    const root = document.createElement("div");
    root.innerHTML = '<button id="a"></button><button disabled></button><input aria-hidden="true" /><a href="#" id="b"></a>';
    const stops = focusableIn(root);
    expect(stops.map((el) => el.id)).toEqual(["a", "b"]);
    const [a, b] = stops as [HTMLElement, HTMLElement];
    expect(wrapTarget(stops, b, false, true)).toBe(a);
    expect(wrapTarget(stops, a, true, true)).toBe(b);
    expect(wrapTarget(stops, a, false, true)).toBeNull();
    expect(wrapTarget(stops, null, false, false)).toBe(a);
    expect(wrapTarget([], null, false, true)).toBeNull();
  });
});
