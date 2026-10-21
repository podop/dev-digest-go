/* ConfigureStep — workflow options: trigger chips (at least one), the secrets the workflow
   reads (static, no status), "Post results as", and how to block merges. Once the workflow
   was edited the options are locked, since changing one would regenerate and drop the edit. */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import { POST_AS_OPTIONS, SECRET_KEYS, TRIGGERS, type PostAs, type Trigger, type TriggerState } from "../../constants";
import { s } from "./styles";

export function ConfigureStep({
  triggers,
  onToggleTrigger,
  postAs,
  onPostAsChange,
  locked,
  slug,
}: {
  triggers: TriggerState;
  onToggleTrigger: (trigger: Trigger) => void;
  postAs: PostAs;
  onPostAsChange: (value: PostAs) => void;
  /** The workflow was edited in Preview. */
  locked: boolean;
  slug: string | null;
}) {
  const t = useTranslations("ci.exportWizard");
  const noneSelected = TRIGGERS.every((tr) => !triggers[tr]);

  return (
    <div style={s.body}>
      {locked && (
        <p role="status" style={s.notice}>
          {t("optionsLockedNotice")}
        </p>
      )}

      <fieldset style={s.group} disabled={locked}>
        <legend style={s.legend}>{t("triggerLabel")}</legend>
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
        {noneSelected && (
          <p role="alert" style={s.invalid}>
            {t("triggerRequired")}
          </p>
        )}
      </fieldset>

      <section style={s.group} aria-label={t("secretsTitle")}>
        <h3 style={s.legend}>{t("secretsTitle")}</h3>
        <ul style={s.secrets}>
          {SECRET_KEYS.map((key) => (
            <li key={key} style={s.secret}>
              <span className="mono" style={s.secretKey}>
                {key}
              </span>
              <span style={s.muted}>{t(`secrets.${key}`)}</span>
            </li>
          ))}
        </ul>
      </section>

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
            {t(`postAs.${opt.labelKey}`)}
            {opt.value === "github_review" && (
              <Badge color="var(--ok)" bg="var(--bg-hover)">
                {t("recommended")}
              </Badge>
            )}
          </label>
        ))}
      </fieldset>

      <p style={s.muted}>{t("requiredCheckNote", { check: t("checkName", { slug: slug ?? "<slug>" }) })}</p>
    </div>
  );
}
