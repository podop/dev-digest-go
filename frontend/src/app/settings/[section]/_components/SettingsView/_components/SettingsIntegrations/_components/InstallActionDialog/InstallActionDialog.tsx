/* InstallActionDialog — "Install in a repo" (FR3): pick one connected repository (GitHub or GitLab, GitLab C
   FR45; a not-connected GitLab one is listed disabled with its reason, FR40) and ≥ 1 agents, then install them one at a time with the CI export (action "open_pr"), showing the
   PR link or the error per agent; Retry failed re-runs only the failed ones (EC13). Agents of a
   disabled plugin are not offered (FR18); agents on an undefined custom provider are listed
   unselectable with the reason (FR32). One Runner choice applies to the whole run: Self-hosted
   by default once a custom-provider agent is picked, until the user chooses; its labels follow the repository's
   host (FR9). A GitLab install sends `repo_id` and `gitlab_ci`. Escape, the focus trap and focus return come from useDialogFocus; the card behind
   refetches by itself (useInstallCi invalidates it). */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Modal } from "@devdigest/ui";
import { Select } from "@/components/select";
import { isGitLab, repoLabel } from "@/lib/code-host";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useAgents, useCustomProviders, useRepos } from "@/lib/hooks";
import { AgentRow } from "./_components/AgentRow";
import { RunnerField } from "./_components/RunnerField";
import {
  defaultRunner,
  failedIds,
  hasStarted,
  offeredAgents,
  repoRefOf,
  splitRepos,
  type Runner,
} from "./helpers";
import { useInstallRun } from "./useInstallRun";
import { s } from "./styles";

/** Where a repository is connected (Settings › GitHub). */
const ADD_REPO_HREF = "/settings/github";

export function InstallActionDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations("integrationsGithubAction.dialog");
  const tg = useTranslations("integrationsGithubAction.dialog.progressGitlab");
  const repos = useRepos();
  // GitLab C FR45: GitHub and GitLab repositories; the not-connected ones are listed below, unselectable.
  const split = React.useMemo(() => (repos.data ? splitRepos(repos.data) : undefined), [repos.data]);
  const usableRepos = split?.usable;
  const agents = useAgents();
  const customProviders = useCustomProviders();
  // The id of the chosen repository ("" = none): names can repeat across hosts.
  const [repoId, setRepoId] = React.useState("");
  const [picked, setPicked] = React.useState<ReadonlySet<string>>(new Set());
  // null = the user has not chosen: the Runner follows the selection (FR32).
  const [chosenRunner, setChosenRunner] = React.useState<Runner | null>(null);
  const chosen = usableRepos?.find((r) => r.id === repoId);
  const gitlab = isGitLab(chosen);
  const repoRef = React.useMemo(() => (chosen ? repoRefOf(chosen) : { repo: "" }), [chosen]);
  const offers = React.useMemo(
    () => offeredAgents(agents.data ?? [], customProviders.data),
    [agents.data, customProviders.data],
  );
  const order = offers.map((o) => o.agent.id);
  // An agent whose provider got undefined since it was picked is dropped from the run.
  const selected = offers.filter((o) => o.block === null && picked.has(o.agent.id)).map((o) => o.agent.id);
  const runner = chosenRunner ?? defaultRunner(offers, new Set(selected));
  const { runs, busy, run } = useInstallRun(repoRef, runner);
  // A run is a request chain: leaving mid-way would orphan the rest, so Close waits for it.
  const close = () => {
    if (!busy) onClose();
  };
  const ref = useDialogFocus(close);

  const repoOptions = React.useMemo(
    () => [
      { value: "", label: t("repoPlaceholder") },
      ...(usableRepos ?? []).map((r) => ({ value: r.id, label: repoLabel(r) })),
    ],
    [usableRepos, t],
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
        subtitle={t(gitlab ? "subtitleGitlab" : "subtitle")}
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
                disabled={chosen === undefined || selected.length === 0}
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
            {usableRepos ? (
              usableRepos.length === 0 ? (
                <div style={s.muted}>
                  {t("noRepos")}{" "}
                  <Link href={ADD_REPO_HREF} style={s.link}>
                    {t("addRepo")}
                  </Link>
                </div>
              ) : (
                <Select
                  value={repoId}
                  onChange={setRepoId}
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
            {split && split.notConnected.length > 0 && (
              <ul aria-label={t("notConnectedLegend")} style={s.unavailable}>
                {split.notConnected.map((r) => (
                  <li key={r.id} aria-disabled="true" style={s.unavailableRow}>
                    <span className="mono">{repoLabel(r)}</span>
                    <span>{t("notConnected", { host: r.host ?? "" })}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <RunnerField value={runner} gitlab={gitlab} locked={busy || started} onChange={setChosenRunner} />

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
                      gitlab={gitlab}
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
          {gitlab && Object.values(runs).some((r) => r.status === "done") && (
            <p style={s.muted}>{tg("pipelinesMustSucceed")}</p>
          )}
        </div>
      </Modal>
    </div>
  );
}
