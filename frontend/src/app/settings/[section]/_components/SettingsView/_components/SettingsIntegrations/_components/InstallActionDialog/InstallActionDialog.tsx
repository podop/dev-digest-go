/* InstallActionDialog — "Install in a repo" (FR3): pick one connected repository and ≥ 1
   agents, then install them one at a time with the CI export (action "open_pr"), showing the
   PR link or the error per agent; Retry failed re-runs only the failed ones (EC13). Agents of a
   disabled plugin are not offered (FR18); agents on an undefined custom provider are listed
   unselectable with the reason (FR32). One Runner choice applies to the whole run: Self-hosted
   by default once a custom-provider agent is picked, until the user chooses. Escape, the focus trap and focus return come from useDialogFocus; the card behind
   refetches by itself (useInstallCi invalidates it). */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Modal } from "@devdigest/ui";
import { Select } from "@/components/select";
import { isGitLab } from "@/lib/code-host";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useAgents, useCustomProviders, useRepos } from "@/lib/hooks";
import { AgentRow } from "./_components/AgentRow";
import { RunnerField } from "./_components/RunnerField";
import { defaultRunner, failedIds, hasStarted, offeredAgents, type Runner } from "./helpers";
import { useInstallRun } from "./useInstallRun";
import { s } from "./styles";

/** Where a repository is connected (Settings › GitHub). */
const ADD_REPO_HREF = "/settings/github";

export function InstallActionDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations("integrationsGithubAction.dialog");
  const repos = useRepos();
  // SCR3: the Action is installed into GitHub repositories only; GitLab ones are not offered.
  const githubRepos = React.useMemo(() => repos.data?.filter((r) => !isGitLab(r)), [repos.data]);
  const agents = useAgents();
  const customProviders = useCustomProviders();
  const [repo, setRepo] = React.useState("");
  const [picked, setPicked] = React.useState<ReadonlySet<string>>(new Set());
  // null = the user has not chosen: the Runner follows the selection (FR32).
  const [chosenRunner, setChosenRunner] = React.useState<Runner | null>(null);
  const offers = React.useMemo(
    () => offeredAgents(agents.data ?? [], customProviders.data),
    [agents.data, customProviders.data],
  );
  const order = offers.map((o) => o.agent.id);
  // An agent whose provider got undefined since it was picked is dropped from the run.
  const selected = offers.filter((o) => o.block === null && picked.has(o.agent.id)).map((o) => o.agent.id);
  const runner = chosenRunner ?? defaultRunner(offers, new Set(selected));
  const { runs, busy, run } = useInstallRun(repo, runner);
  // A run is a request chain: leaving mid-way would orphan the rest, so Close waits for it.
  const close = () => {
    if (!busy) onClose();
  };
  const ref = useDialogFocus(close);

  const repoOptions = React.useMemo(
    () => [
      { value: "", label: t("repoPlaceholder") },
      ...(githubRepos ?? []).map((r) => ({ value: r.full_name, label: r.full_name })),
    ],
    [githubRepos, t],
  );
  const started = hasStarted(runs);
  const failed = failedIds(runs, order);
  const toggle = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={560}
        title={t("title")}
        subtitle={t("subtitle")}
        onClose={close}
        footer={
          <div style={s.footer}>
            <Button type="button" kind="ghost" onClick={close} disabled={busy}>
              {started && !busy ? t("actions.done") : t("actions.cancel")}
            </Button>
            {started && !busy && failed.length > 0 && (
              <Button type="button" kind="secondary" onClick={() => void run(failed)}>
                {t("actions.retryFailed")}
              </Button>
            )}
            {(!started || busy) && (
              <Button
                type="button"
                kind="primary"
                loading={busy}
                disabled={repo === "" || selected.length === 0}
                onClick={() => {
                  setChosenRunner(runner); // frozen for Retry failed
                  void run(selected);
                }}
              >
                {busy ? t("actions.installing") : t("actions.install")}
              </Button>
            )}
          </div>
        }
      >
        <div style={s.body}>
          <div style={s.field}>
            <span id="gha-install-repo" style={s.label}>
              {t("repoLabel")}
            </span>
            {githubRepos ? (
              githubRepos.length === 0 ? (
                <div style={s.muted}>
                  {t("noRepos")}{" "}
                  <Link href={ADD_REPO_HREF} style={s.link}>
                    {t("addRepo")}
                  </Link>
                </div>
              ) : (
                <Select
                  value={repo}
                  onChange={setRepo}
                  options={repoOptions}
                  aria-label={t("repoLabel")}
                  disabled={busy || started}
                />
              )
            ) : repos.isError ? (
              <ErrorState title={t("reposError")} onRetry={() => void repos.refetch()} retryLabel={t("retry")} />
            ) : (
              <div role="status" style={s.muted}>
                {t("reposLoading")}
              </div>
            )}
          </div>

          <RunnerField value={runner} locked={busy || started} onChange={setChosenRunner} />

          <fieldset style={s.field}>
            <legend style={s.label}>{t("agentsLegend")}</legend>
            {agents.data ? (
              offers.length === 0 ? (
                <div style={s.muted}>{t("noAgents")}</div>
              ) : (
                <ul style={s.agents}>
                  {offers.map((offer) => (
                    <AgentRow
                      key={offer.agent.id}
                      offer={offer}
                      checked={picked.has(offer.agent.id)}
                      locked={busy || started}
                      run={runs[offer.agent.id]}
                      onToggle={() => toggle(offer.agent.id)}
                    />
                  ))}
                </ul>
              )
            ) : agents.isError ? (
              <ErrorState title={t("agentsError")} onRetry={() => void agents.refetch()} retryLabel={t("retry")} />
            ) : (
              <div role="status" style={s.muted}>
                {t("agentsLoading")}
              </div>
            )}
          </fieldset>
        </div>
      </Modal>
    </div>
  );
}
