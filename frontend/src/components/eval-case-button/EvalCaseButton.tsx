/* EvalCaseButton — "Turn into eval case" on a FindingCard (eval-pipeline FR20).
   Enabled only for a decided finding of an agent's review with no case; once a
   case exists it becomes an "Eval case ✓" link to the agent's Evals tab. A disabled
   state says why (tooltip on a wrapping span — a disabled button shows none). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import type { FindingRecord } from "@devdigest/shared";
import { useCreateEvalCase } from "@/lib/hooks";
import { evalCaseBlock, evalCaseErrorKey, evalsTabHref } from "./helpers";
import { s } from "./styles";

export function EvalCaseButton({
  finding,
  agentId,
  prId,
}: {
  finding: FindingRecord;
  /** The review's agent; null for seed reviews / deleted agents. */
  agentId: string | null | undefined;
  prId: string;
}) {
  const t = useTranslations("prReview");
  const create = useCreateEvalCase(prId);

  if (finding.eval_case_id || create.isSuccess) {
    const label = (
      <>
        <Icon.Check size={13} aria-hidden />
        {t("finding.evalCaseDone")}
      </>
    );
    return agentId ? (
      <Link href={evalsTabHref(agentId)} style={s.doneLink} title={t("finding.evalCaseDoneHint")}>
        {label}
      </Link>
    ) : (
      <span style={s.doneLink}>{label}</span>
    );
  }

  const block = evalCaseBlock(finding, agentId);
  const blockHint = block === "no_agent" ? t("finding.evalCaseNoAgent") : block === "undecided" ? t("finding.evalCaseUndecided") : undefined;

  return (
    <>
      <span title={blockHint ?? t("finding.evalCaseHint")}>
        <Button
          kind="ghost"
          size="sm"
          icon="FlaskConical"
          disabled={block !== null}
          loading={create.isPending}
          onClick={() => agentId && create.mutate({ findingId: finding.id, agentId })}
        >
          {t("finding.evalCase")}
        </Button>
      </span>
      {create.isError && (
        <div role="alert" style={s.error}>
          {t(`finding.evalCaseError.${evalCaseErrorKey(create.error)}`)}
        </div>
      )}
    </>
  );
}
