/* RunSteps — body of the "How to run locally" section: numbered steps, each with its command
   (wraps, never scrolls the page), the comment, and a copy button that copies the command only
   and confirms for 2 s. A rejected/unavailable clipboard raises a "Copy failed" toast. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { OnboardingRunStep } from "@devdigest/shared";
import { useToast } from "@/lib/toast";
import { COPIED_MS } from "./constants";
import { copyText } from "@/lib/clipboard";
import { s } from "./styles";

export function RunSteps({ steps }: { steps: readonly OnboardingRunStep[] }) {
  const t = useTranslations("onboarding");
  const toast = useToast();
  /** Index of the step whose command was just copied. */
  const [copied, setCopied] = React.useState<number | null>(null);

  React.useEffect(() => {
    if (copied === null) return;
    const id = setTimeout(() => setCopied(null), COPIED_MS);
    return () => clearTimeout(id);
  }, [copied]);

  const onCopy = async (index: number, command: string) => {
    if (await copyText(command)) setCopied(index);
    else toast.error(t("run.copyFailed"));
  };

  if (steps.length === 0) return <p style={s.empty}>{t("run.empty")}</p>;

  return (
    <>
      <p style={s.warning}>{t("run.reviewWarning")}</p>
      <ol aria-label={t("run.list")} style={s.list}>
        {steps.map((step, i) => {
          const done = copied === i;
          return (
            <li key={i} style={s.row}>
              <span aria-hidden style={s.num}>
                {i + 1}
              </span>
              <div style={s.text}>
                <code className="mono" style={s.command}>
                  {step.command}
                </code>
                {step.comment && <span style={s.comment}># {step.comment}</span>}
              </div>
              {done && (
                <span role="status" style={s.copiedText}>
                  {t("run.copied")}
                </span>
              )}
              <button
                type="button"
                aria-label={t("run.copy", { n: i + 1 })}
                title={t("run.copy", { n: i + 1 })}
                onClick={() => void onCopy(i, step.command)}
                style={{ ...s.copyBtn, ...(done ? s.copyDone : null) }}
              >
                {done ? <Icon.Check size={14} /> : <Icon.Copy size={14} />}
              </button>
            </li>
          );
        })}
      </ol>
    </>
  );
}
