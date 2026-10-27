/* RepoPicker — every repository of the workspace with its host label, as a radio list keyed by
   repository id (two hosts can share a name). A GitLab repository without a connection is listed
   disabled and carries its reason as text (FR40, NFR11). Names and hosts are plain text. */
"use client";

import { useTranslations } from "next-intl";
import type { Repo } from "@/lib/types";
import { isNotConnected, repoLabel } from "@/lib/code-host";
import { s } from "./styles";

export function RepoPicker({
  repos,
  selectedId,
  onChange,
}: {
  repos: readonly Repo[];
  selectedId: string | null;
  onChange: (id: string) => void;
}) {
  const t = useTranslations("ci.exportWizard");

  return (
    <fieldset style={s.group}>
      <legend style={s.legend}>{t("repoLabel")}</legend>
      <p style={s.hint}>{t("repoHint")}</p>
      <div style={s.list}>
        {repos.map((r) => {
          const off = isNotConnected(r);
          const reasonId = `ci-repo-${r.id}-reason`;
          return (
            <div key={r.id} style={s.row}>
              <label style={off ? s.labelOff : s.label}>
                <input
                  type="radio"
                  name="ci-repo"
                  value={r.id}
                  checked={r.id === selectedId}
                  disabled={off}
                  aria-describedby={off ? reasonId : undefined}
                  onChange={() => onChange(r.id)}
                />
                <span className="mono">{repoLabel(r)}</span>
              </label>
              {off && (
                <span id={reasonId} style={s.reason}>
                  {t("gitlab.notConnected", { host: r.host ?? "" })}
                </span>
              )}
            </div>
          );
        })}
      </div>
    </fieldset>
  );
}
