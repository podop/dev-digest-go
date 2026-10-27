/* ConfigureStep — workflow options: trigger chips (at least one), the Runner (with the
   custom provider's self-hosted note), the secrets and variables the workflow needs (from the
   export response), "Post results as", and how to block merges. Once the workflow was edited
   the options are locked, since changing one would regenerate and drop the edit. For GitLab the
   trigger chips are replaced by the note that MR pipelines run on every push, and the labels
   say MR / GitLab runners (GitLab C FR42). */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import {
  POST_AS_OPTIONS,
  RUNNER_OPTIONS,
  TRIGGERS,
  type PostAs,
  type Runner,
  type Trigger,
  type TriggerState,
} from "../../constants";
import type { Requirements as RequirementsData } from "../../helpers";
import { Requirements } from "../Requirements";
import { s } from "./styles";

export function ConfigureStep({
  gitlab,
  triggers,
  onToggleTrigger,
  postAs,
  onPostAsChange,
  runner,
  onRunnerChange,
  requirements,
  locked,
  slug,
}: {
  /** The repository is on GitLab. */
  gitlab: boolean;
  triggers: TriggerState;
  onToggleTrigger: (trigger: Trigger) => void;
  postAs: PostAs;
  onPostAsChange: (value: PostAs) => void;
  runner: Runner;
  onRunnerChange: (value: Runner) => void;
  /** Secrets and variables from the preview response, and the agent's custom provider. */
  requirements: RequirementsData;
  /** The workflow was edited in Preview. */
  locked: boolean;
  slug: string | null;
}) {
  const t = useTranslations("ci.exportWizard");
  const noneSelected = !gitlab && TRIGGERS.every((tr) => !triggers[tr]);
  const host = gitlab ? "gitlab." : "";

  return (
    <div style={s.body}>
      {locked && (
        <p role="status" style={s.notice}>
          {t("optionsLockedNotice")}
        </p>
      )}

      <fieldset style={s.group} disabled={locked}>
        <legend style={s.legend}>{t("triggerLabel")}</legend>
        {gitlab ? (
          <p style={s.muted}>{t("gitlab.triggersNote")}</p>
        ) : (
          <div style={s.chips}>
            {TRIGGERS.map((tr) => (
              <button
                key={tr}
                type="button"
                className="mono"
                aria-pressed={triggers[tr]}
                disabled={locked}
                onClick={() => onToggleTrigger(tr)}
                style={triggers[tr] ? { ...s.chip, ...s.chipOn } : s.chip}
              >
                {`pull_request:${tr}`}
              </button>
            ))}
          </div>
        )}
        {noneSelected && (
          <p role="alert" style={s.invalid}>
            {t("triggerRequired")}
          </p>
        )}
      </fieldset>

      <fieldset style={s.group} disabled={locked}>
        <legend style={s.legend}>{t("runnerLabel")}</legend>
        {RUNNER_OPTIONS.map((opt) => (
          <label key={opt.value} style={s.radio}>
            <input
              type="radio"
              name="ci-runner"
              value={opt.value}
              checked={runner === opt.value}
              disabled={locked}
              onChange={() => onRunnerChange(opt.value)}
            />
            {t(`${host}runner.${opt.labelKey}`)}
          </label>
        ))}
        {requirements.custom && (
          <p style={s.muted}>
            {t("selfHostedNote", { name: requirements.custom.name, host: requirements.custom.host })}
          </p>
        )}
      </fieldset>

      <Requirements {...requirements} />

      <fieldset style={s.group} disabled={locked}>
        <legend style={s.legend}>{t("postResultsLabel")}</legend>
        {POST_AS_OPTIONS.map((opt) => (
          <label key={opt.value} style={s.radio}>
            <input
              type="radio"
              name="ci-post-as"
              value={opt.value}
              checked={postAs === opt.value}
              disabled={locked}
              onChange={() => onPostAsChange(opt.value)}
            />
            {t(`${host}postAs.${opt.labelKey}`)}
            {opt.value === "github_review" && (
              <Badge color="var(--ok)" bg="var(--bg-hover)">
                {t("recommended")}
              </Badge>
            )}
          </label>
        ))}
      </fieldset>

      <p style={s.muted}>
        {gitlab
          ? t("gitlab.requiredCheckNote")
          : t("requiredCheckNote", { check: t("checkName", { slug: slug ?? "<slug>" }) })}
      </p>
    </div>
  );
}
