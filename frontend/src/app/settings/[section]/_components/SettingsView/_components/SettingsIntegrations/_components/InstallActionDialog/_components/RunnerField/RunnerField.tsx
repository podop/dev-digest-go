/* RunnerField — the dialog's one Runner choice (FR32): GitHub-hosted or Self-hosted (linux x64).
   Every agent of the run is exported with it. Locked once the run started. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { RUNNERS, type Runner } from "../../helpers";
import { s } from "./styles";

export function RunnerField({
  value,
  locked,
  onChange,
}: {
  value: Runner;
  locked: boolean;
  onChange: (runner: Runner) => void;
}) {
  const t = useTranslations("integrationsGithubAction.dialog.runner");
  const name = React.useId();

  return (
    <fieldset style={s.field}>
      <legend style={s.legend}>{t("legend")}</legend>
      <div style={s.options}>
        {RUNNERS.map((runner) => (
          <label key={runner} style={s.option(locked)}>
            <input
              type="radio"
              name={name}
              value={runner}
              checked={value === runner}
              disabled={locked}
              onChange={() => onChange(runner)}
            />
            {t(runner)}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
