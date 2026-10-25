/* SetupView — first-run setup (FR22–FR26): a centred card with a 3-step stepper (model
   provider key → GitHub token → first repository). The step shown comes from the URL, pulled
   back to the first incomplete step; completeness comes from secrets-status and GET /repos,
   so nothing new is stored. Step changes replace the URL. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { useRepos, useSecretsStatus } from "@/lib/hooks";
import type { ConnTestProvider } from "@/lib/types";
import { SETUP_STEPS, pullsHref, setupHref, type SetupStep } from "./constants";
import { nextStep, previousStep, resolveStep, stepsDone } from "./helpers";
import { GithubStep } from "./_components/GithubStep";
import { ModelStep } from "./_components/ModelStep";
import { RepoStep } from "./_components/RepoStep";
import { Stepper } from "./_components/Stepper";
import { s } from "./styles";

export function SetupView({ requestedStep }: { requestedStep?: string }) {
  const t = useTranslations("setup");
  const router = useRouter();
  const secrets = useSecretsStatus();
  const repos = useRepos();
  // Providers whose test passed in this view (a passing test stores the key).
  const [tested, setTested] = React.useState<ConnTestProvider[]>([]);

  const markTested = React.useCallback(
    (provider: ConnTestProvider) => setTested((prev) => (prev.includes(provider) ? prev : [...prev, provider])),
    [],
  );

  const done = secrets.data && repos.data ? stepsDone(secrets.data, repos.data.length, tested) : null;
  const step = done ? resolveStep(requestedStep, done) : null;

  // Pin the resolved step in the URL (a missing, invalid or too-far `step` is corrected), so a step
  // that completes while it is shown does not make the view jump ahead.
  React.useEffect(() => {
    if (step && step !== requestedStep) router.replace(setupHref(step));
  }, [step, requestedStep, router]);

  let body: React.ReactNode;
  if (secrets.isError || repos.isError) {
    body = (
      <ErrorState
        title={t("error.title")}
        body={t("error.body")}
        retryLabel={t("error.retry")}
        onRetry={() => {
          if (secrets.isError) void secrets.refetch();
          if (repos.isError) void repos.refetch();
        }}
      />
    );
  } else if (!secrets.data || !repos.data || !done || !step) {
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
    const go = (to: SetupStep | null) => (to ? () => router.replace(setupHref(to)) : undefined);
    body = (
      <>
        <Stepper current={step} done={done} />
        {step === "model" && (
          <ModelStep secrets={secrets.data} onTested={markTested} onContinue={go(next)} />
        )}
        {step === "github" && (
          <GithubStep secrets={secrets.data} onTested={markTested} onBack={go(prev)} onContinue={go(next)} />
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
    <main style={s.page}>
      <div style={s.brand}>
        <div style={s.logo}>
          <Icon.Layers size={17} style={{ color: "var(--bg-primary)" }} />
        </div>
        <span style={s.brandName}>{t("brand")}</span>
      </div>
      <div style={s.card}>{body}</div>
      {step && <p style={s.footer}>{t("footer", { n: SETUP_STEPS.indexOf(step) + 1 })}</p>}
    </main>
  );
}
