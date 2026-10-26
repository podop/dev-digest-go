/* SelectionList — one multi-select group (a fieldset) of the export dialog (FR15): a checkbox per agent or
   skill, "Select all" / "Clear" for the group. The selection is kept as the EXCLUDED ids, so
   "everything selected" is the empty set and lists that load later start fully selected. */
"use client";

import { useTranslations } from "next-intl";
import { Button, Checkbox } from "@devdigest/ui";
import { s } from "./styles";

export interface SelectionItem {
  id: string;
  name: string;
}

interface Props {
  heading: string;
  emptyText: string;
  items: readonly SelectionItem[];
  excluded: ReadonlySet<string>;
  onChange: (excluded: ReadonlySet<string>) => void;
  disabled?: boolean;
}

export function SelectionList({ heading, emptyText, items, excluded, onChange, disabled = false }: Props) {
  const t = useTranslations("integrationsExport.dialog.selection");
  const allSelected = items.every((item) => !excluded.has(item.id));

  const toggle = (id: string, selected: boolean) => {
    const next = new Set(excluded);
    if (selected) next.delete(id);
    else next.add(id);
    onChange(next);
  };

  return (
    <fieldset style={s.group}>
      <legend style={s.heading}>{heading}</legend>
      {items.length > 0 && (
        <div style={s.toggle}>
          <Button
            type="button"
            kind="tertiary"
            size="sm"
            disabled={disabled}
            onClick={() => onChange(allSelected ? new Set(items.map((item) => item.id)) : new Set())}
          >
            {allSelected ? t("clearAll") : t("selectAll")}
          </Button>
        </div>
      )}
      {items.length === 0 ? (
        <p style={s.empty}>{emptyText}</p>
      ) : (
        <ul style={s.list}>
          {items.map((item) => (
            <li key={item.id} style={s.item}>
              <Checkbox
                checked={!excluded.has(item.id)}
                onChange={(selected) => !disabled && toggle(item.id, selected)}
                label={
                  <span title={item.name} style={s.itemName}>
                    {item.name}
                  </span>
                }
              />
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  );
}
