import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Modal } from "@devdigest/ui";
import { Select } from "./Select";

afterEach(cleanup);

const OPTIONS = ["openai", "anthropic", "openrouter"];

describe("Select", () => {
  it("click opens the listbox, marks the selected option, and picking one calls onChange and closes", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });

    await user.click(combobox);
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(screen.getAllByRole("option")).toHaveLength(3);
    expect(screen.getByRole("option", { name: "openai" })).toHaveAttribute("aria-selected", "true");

    await user.click(screen.getByRole("option", { name: "anthropic" }));
    expect(onChange).toHaveBeenCalledWith("anthropic");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("formatValue changes only the trigger text; the options keep their bare labels", async () => {
    const user = userEvent.setup();
    render(
      <Select value="openai" options={OPTIONS} formatValue={(label) => `Provider: ${label}`} aria-label="Provider" />,
    );
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    expect(combobox).toHaveTextContent("Provider: openai");

    await user.click(combobox);
    expect(screen.getByRole("option", { name: "openai" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Provider: openai" })).not.toBeInTheDocument();
  });

  it("ArrowDown x2 + Enter picks openrouter; Escape closes with no call and keeps focus on the combobox", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });

    // The first ArrowDown opens the list at the selected option (openai); the second
    // moves one step down (anthropic); Enter picks it.
    combobox.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(onChange).toHaveBeenCalledWith("anthropic");

    onChange.mockClear();
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(combobox).toHaveFocus();
  });

  it("Home/End jump to the ends; type-ahead 'a' then 'n' picks anthropic", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });

    combobox.focus();
    await user.keyboard("{End}");
    expect(combobox).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "openrouter" }).id,
    );
    await user.keyboard("{Home}");
    expect(combobox).toHaveAttribute("aria-activedescendant", screen.getByRole("option", { name: "openai" }).id);

    await user.keyboard("an{Enter}");
    expect(onChange).toHaveBeenCalledWith("anthropic");
  });

  it("closes on an outside mousedown; inside a Modal the option renders outside the dialog DOM and picking works", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(
      <div>
        <Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />
        <button type="button">outside</button>
      </div>,
    );
    await user.click(screen.getByRole("combobox", { name: "Provider" }));
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "outside" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    unmount();

    render(
      <Modal title="Agent">
        <Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />
      </Modal>,
    );
    await user.click(screen.getByRole("combobox", { name: "Provider" }));
    const dialog = screen.getByRole("dialog");
    const option = screen.getByRole("option", { name: "anthropic" });
    expect(dialog.contains(option)).toBe(false);

    await user.click(option);
    expect(onChange).toHaveBeenCalledWith("anthropic");
  });

  it("aria-haspopup, aria-expanded and aria-controls stay wired to open state", async () => {
    const user = userEvent.setup();
    render(<Select value="openai" options={OPTIONS} aria-label="Provider" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    expect(combobox).toHaveAttribute("aria-haspopup", "listbox");
    expect(combobox).toHaveAttribute("aria-expanded", "false");

    await user.click(combobox);
    const listbox = screen.getByRole("listbox");
    expect(combobox).toHaveAttribute("aria-expanded", "true");
    expect(combobox).toHaveAttribute("aria-controls", listbox.id);

    await user.click(combobox);
    expect(combobox).toHaveAttribute("aria-expanded", "false");
  });

  it("disabled: aria-disabled is set, the trigger leaves the tab order, and clicks/keys are no-ops", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" disabled />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    expect(combobox).toHaveAttribute("aria-disabled", "true");
    expect(combobox).toHaveAttribute("tabindex", "-1");

    await user.click(combobox);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();

    combobox.focus();
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("Tab while the list is open closes it without picking and lets focus move on", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <div>
        <Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />
        <button type="button">next</button>
      </div>,
    );
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    combobox.focus();
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("listbox")).toBeInTheDocument();

    await user.tab();
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "next" })).toHaveFocus();
  });

  it("Space opens the closed trigger and, once open, picks the active option like Enter", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Select value="openai" onChange={onChange} options={OPTIONS} aria-label="Provider" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    combobox.focus();

    await user.keyboard(" ");
    expect(screen.getByRole("listbox")).toBeInTheDocument();

    await user.keyboard("{ArrowDown} ");
    expect(onChange).toHaveBeenCalledWith("anthropic");
  });

  it("mono (default true) sets the JetBrains Mono trigger class; mono=false drops it", () => {
    const { rerender } = render(<Select value="openai" options={OPTIONS} aria-label="Provider" />);
    expect(screen.getByRole("combobox", { name: "Provider" })).toHaveClass("mono");

    rerender(<Select value="openai" options={OPTIONS} aria-label="Provider" mono={false} />);
    expect(screen.getByRole("combobox", { name: "Provider" })).not.toHaveClass("mono");
  });

  it("icon renders a decorative glyph on the trigger and before every option; chevron picks the indicator", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<Select value="openai" options={OPTIONS} aria-label="Provider" icon="Cpu" chevron="down" />);
    const combobox = screen.getByRole("combobox", { name: "Provider" });
    expect(screen.getByTestId("select-trigger-icon")).toHaveAttribute("aria-hidden", "true");
    expect(combobox.querySelectorAll("svg")).toHaveLength(2); // glyph + single down chevron

    await user.click(combobox);
    const icons = screen.getAllByTestId("select-option-icon");
    expect(icons).toHaveLength(OPTIONS.length);
    expect(screen.getByRole("option", { name: "anthropic" })).toContainElement(icons[1]!);
    await user.keyboard("{Escape}");

    rerender(<Select value="openai" options={OPTIONS} aria-label="Provider" icon="Calendar" chevron="none" />);
    expect(combobox.querySelectorAll("svg")).toHaveLength(1); // glyph only
    rerender(<Select value="openai" options={OPTIONS} aria-label="Provider" />);
    expect(screen.queryByTestId("select-trigger-icon")).not.toBeInTheDocument();
    expect(combobox.querySelectorAll("svg")).toHaveLength(1); // default up/down pair
  });
});
