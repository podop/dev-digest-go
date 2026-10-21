/* DocList — the document rows of the Project Context screen: file icon, name,
   folder and a doc-type badge. Long names are truncated, the full path is in the tooltip. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, Icon } from "@devdigest/ui";
import type { ContextDoc } from "@devdigest/shared";
import { splitPath } from "../../helpers";
import { s } from "./styles";

export function DocList({
  docs,
  selected,
  onSelect,
}: {
  docs: readonly ContextDoc[];
  selected: string | null;
  onSelect: (path: string) => void;
}) {
  const t = useTranslations("context");
  return (
    <ul aria-label={t("docList")} style={s.list}>
      {docs.map((d) => {
        const { name, folder } = splitPath(d.path);
        const active = d.path === selected;
        return (
          <li key={d.path}>
            <button
              type="button"
              title={d.path}
              aria-current={active ? "true" : undefined}
              onClick={() => onSelect(d.path)}
              style={{ ...s.rowBase, ...(active ? s.rowActive : null) }}
            >
              <Icon.FileText size={14} style={active ? s.iconActive : s.icon} />
              <span style={s.text}>
                <span className="mono" style={s.name}>
                  {name}
                </span>
                {folder && <span style={s.folder}>{folder}</span>}
              </span>
              <span style={s.badge}>
                <Badge>{t(`docType.${d.doc_type}`)}</Badge>
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}
