/* SetupView — first-run setup flow (FR22–FR26): a 3-step stepper (model provider key → code host:
   GitHub token or GitLab connection → repository) with the step's content and a "Step n of 3" footer.
   It has no page frame: it is rendered inline by Settings › First-run setup. The step shown comes from
   `requestedStep`, pulled back to the first incomplete step; completeness comes from secrets-status,
   GET /repos, the custom providers and the GitLab connections, so nothing new is stored. Step
   changes replace the URL with `hrefForStep(step)`. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { ErrorState, Skeleton } from "@devdigest/ui";
import { useCustomProviders, useGitLabConnections, useRepos, useSecretsStatus } from "@/lib/hooks";
import type { ConnTestProvider } from "@/lib/types";
import { SETUP_STEPS, pullsHref, type SetupStep } from "./constants";
import { gitlabTokenCount, nextStep, previousStep, resolveStep, stepsDone } from "./helpers";
import { CodeHostStep } from "./_components/CodeHostStep";
import { ModelStep } from "./_components/ModelStep";
import { RepoStep } from "./_components/RepoStep";
import { Stepper } from "./_components/Stepper";
import { s } from "./styles";

interface SetupViewProps {
  /** The `step` the URL asks for (any string; resolved against what is complete). */
  requestedStep?: string;
  /** The URL of a step, used to pin the resolved step and to move between steps. */
  hrefForStep: (step: SetupStep) => string;
}

export function SetupView({ requestedStep, hrefForStep }: SetupViewProps) {
  const t = useTranslations("setup");
  const router = useRouter();
  const secrets = useSecretsStatus();
  const repos = useRepos();
  const custom = useCustomProviders();
  const gitlab = useGitLabConnections();
  // Providers whose test passed in this view (a passing test stores the key).
  const [tested, setTested] = React.useState<ConnTestProvider[]>([]);

  const markTested = React.useCallback(
    (provider: ConnTestProvider) => setTested((prev) => (prev.includes(provider) ? prev : [...prev, provider])),
    [],
  );

  const done =
    secrets.data && repos.data && custom.data && gitlab.data
      ? stepsDone(secrets.data, repos.data.length, tested, custom.data.length, gitlabTokenCount(gitlab.data))
      : null;
  const step = done ? resolveStep(requestedStep, done) : null;

  // Pin the resolved step in the URL (a missing, invalid or too-far `step` is corrected), so a step
  // that completes while it is shown does not make the view jump ahead.
  React.useEffect(() => {
    if (step && step !== requestedStep) router.replace(hrefForStep(step));
  }, [step, requestedStep, router, hrefForStep]);

  let body: React.ReactNode;
  if (secrets.isError || repos.isError || custom.isError || gitlab.isError) {
    body = (
      <ErrorState
        title={t("error.title")}
        body={t("error.body")}
        retryLabel={t("error.retry")}
        onRetry={() => {
          if (secrets.isError) void secrets.refetch();
          if (repos.isError) void repos.refetch();
          if (custom.isError) void custom.refetch();
          if (gitlab.isError) void gitlab.refetch();
        }}
      />
    );
  } else if (!secrets.data || !repos.data || !custom.data || !gitlab.data || !done || !step) {
    body = (
      <div style={s.skeletons} role="status" aria-busy="true" aria-label={t("loading")}>
        <Skeleton height={24} width={260} />
        <Skeleton height={44} />
        <Skeleton height={44} />
      </div>
    );
  } else {
    const prev = previousStep(step);
    const next = nextStep(step);
    const go = (to: SetupStep | null) => (to ? () => router.replace(hrefForStep(to)) : undefined);
    body = (
      <>
        <Stepper current={step} done={done} />
        {step === "model" && (
          <ModelStep secrets={secrets.data} customCount={custom.data.length} onTested={markTested} onContinue={go(next)} />
        )}
        {step === "code-host" && (
          <CodeHostStep
            secrets={secrets.data}
            gitlabConnections={gitlab.data}
            onTested={markTested}
            onBack={go(prev)}
            onContinue={go(next)}
          />
        )}
        {step === "repo" && (
          <RepoStep
            repos={repos.data}
            onBack={go(prev)}
            onFinish={(repoId) => router.push(pullsHref(repoId))}
          />
        )}
      </>
    );
  }

  return (
    <div>
      {body}
      {step && <p style={s.footer}>{t("footer", { n: SETUP_STEPS.indexOf(step) + 1 })}</p>}
    </div>
  );
}
