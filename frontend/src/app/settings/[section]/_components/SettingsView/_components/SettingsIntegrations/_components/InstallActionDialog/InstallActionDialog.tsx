/* InstallActionDialog — "Install in a repo" (FR3): pick one connected repository and ≥ 1
   agents, then install them one at a time with the CI export (action "open_pr"), showing the
   PR link or the error per agent; Retry failed re-runs only the failed ones (EC13). Agents of a
   disabled plugin are not offered (FR18); non-OpenRouter ones are listed unselectable with the
   reason. Escape, the focus trap and focus return come from useDialogFocus; the card behind
   refetches by itself (useInstallCi invalidates it). */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Modal } from "@devdigest/ui";
import { Select } from "@/components/select";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useAgents, useRepos } from "@/lib/hooks";
import { AgentRow } from "./_components/AgentRow";
import { failedIds, hasStarted, offeredAgents } from "./helpers";
import { useInstallRun } from "./useInstallRun";
import { s } from "./styles";

/** Where a repository is connected (Settings › GitHub). */
const ADD_REPO_HREF = "/settings/github";

export function InstallActionDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations("integrationsGithubAction.dialog");
  const repos = useRepos();
  const agents = useAgents();
  const [repo, setRepo] = React.useState("");
  const [picked, setPicked] = React.useState<ReadonlySet<string>>(new Set());
  const { runs, busy, run } = useInstallRun(repo);
  // A run is a request chain: leaving mid-way would orphan the rest, so Close waits for it.
  const close = () => {
    if (!busy) onClose();
  };
  const ref = useDialogFocus(close);

  const offers = React.useMemo(() => offeredAgents(agents.data ?? []), [agents.data]);
  const order = offers.map((o) => o.agent.id);
  const repoOptions = React.useMemo(
    () => [
      { value: "", label: t("repoPlaceholder") },
      ...(repos.data ?? []).map((r) => ({ value: r.full_name, label: r.full_name })),
    ],
    [repos.data, t],
  );
  const started = hasStarted(runs);
  const failed = failedIds(runs, order);
  const selected = order.filter((id) => picked.has(id));
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
                onClick={() => void run(selected)}
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
            {repos.data ? (
              repos.data.length === 0 ? (
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
