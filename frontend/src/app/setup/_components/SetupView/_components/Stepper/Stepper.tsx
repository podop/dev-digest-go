"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { SETUP_STEPS, type SetupStep } from "../../constants";
import type { StepDone } from "../../helpers";
import { s } from "./styles";

/** The 3-step indicator (FR22): done = check, current = number + `aria-current="step"`, future = muted. */
export function Stepper({ current, done }: { current: SetupStep; done: StepDone }) {
  const t = useTranslations("setup");
  return (
    <ol aria-label={t("stepper.label")} style={s.list}>
      {SETUP_STEPS.map((step, i) => {
        const state = step === current ? "current" : done[step] ? "done" : "future";
        return (
          <li
            key={step}
            aria-current={state === "current" ? "step" : undefined}
            style={s.item(state, i < SETUP_STEPS.length - 1)}
          >
            <span style={s.dot(state)} aria-hidden="true">
              {state === "done" ? <Icon.Check size={13} /> : i + 1}
            </span>
            {t(`stepper.${step}`)}
            {state === "done" && <span style={s.srOnly}>{t("stepper.done")}</span>}
            {i < SETUP_STEPS.length - 1 && <span style={s.line} aria-hidden="true" />}
          </li>
        );
      })}
    </ol>
  );
}
