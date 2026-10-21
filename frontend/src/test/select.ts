/* pickOption — the Select equivalent of `userEvent.selectOptions` for the
   native <select>. Select's options render in a document.body portal, so they
   can't be queried `within` the combobox's container; this always goes
   through `screen`. */
import { screen } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

export async function pickOption(user: UserEvent, combobox: HTMLElement, name: string): Promise<void> {
  await user.click(combobox);
  await user.click(screen.getByRole("option", { name }));
}
