/* ExportWizard — the 4-step Export-to-CI dialog: Target → Preview → Configure → Install.
   The vendored Modal gives role="dialog" and a close button; Escape, the focus trap and
   focus return come from useDialogFocus. Closing discards everything (no draft is kept). */
"use client";

import { useTranslations } from "next-intl";
import { Button, ExportWizardSteps, Modal } from "@devdigest/ui";
import { LAST_STEP, MODAL_WIDTH, STEP_KEYS } from "./constants";
import { describeError, type Requirements } from "./helpers";
import { s } from "./styles";
import { useDialogFocus, useExportWizard } from "./useExportWizard";
import { ConfigureStep } from "./_components/ConfigureStep";
import { InstallStep } from "./_components/InstallStep";
import { PreviewStep } from "./_components/PreviewStep";
import { TargetStep } from "./_components/TargetStep";

export function ExportWizard({
  agentId,
  agentName,
  provider,
  initialRepo,
  onClose,
}: {
  agentId: string;
  agentName: string;
  /** The agent's provider id (`openrouter`, `openai`, `anthropic` or `custom:<slug>`). */
  provider: string;
  /** A row's "Update" preselects its repository. */
  initialRepo?: string;
  onClose: () => void;
}) {
  const t = useTranslations("ci.exportWizard");
  const w = useExportWizard({ agentId, provider, initialRepo });
  const ref = useDialogFocus(onClose);
  const fallback = t("unknownError");
  const { step, openPr } = w;

  const previewError = w.preview.isError ? describeError(w.preview.error, fallback) : null;
  const installError = openPr.isError ? describeError(openPr.error, fallback) : null;
  const edited = w.edited != null;
  const installed = openPr.isSuccess;
  // The PR response lists the same names as the preview; either one is the server's answer.
  const exported = openPr.data ?? w.preview.data;
  const requirements: Requirements = {
    gitlab: w.gitlab,
    secrets: exported?.required_secrets ?? [],
    variables: exported?.required_variables ?? [],
    custom: w.custom,
  };

  return (
    <div ref={ref} style={s.contents}>
      <Modal
        width={MODAL_WIDTH}
        title={t("title")}
        subtitle={t("subtitle", { agentName: agentName || t("thisAgent") })}
        onClose={onClose}
        footer={
          <div style={s.footer}>
            <Button kind="ghost" onClick={w.back} disabled={step === 0 || openPr.isPending}>
              {t("back")}
            </Button>
            <div style={s.footerRight}>
              {installed && (
                <Button kind="primary" onClick={onClose}>
                  {t("close")}
                </Button>
              )}
              {!installed && step < LAST_STEP && (
                <Button kind="primary" onClick={w.next} disabled={!w.canContinue}>
                  {t("continue")}
                </Button>
              )}
              {!installed && step === LAST_STEP && (
                <Button
                  kind="primary"
                  onClick={w.install}
                  disabled={openPr.isPending || !w.files}
                  loading={openPr.isPending}
                >
                  {openPr.isPending ? t("installing") : t("install")}
                </Button>
              )}
            </div>
          </div>
        }
      >
        <div style={s.stepsBar}>
          <ExportWizardSteps step={step} labels={STEP_KEYS.map((key) => t(`steps.${key}`))} />
        </div>

        {step === 0 && (
          <TargetStep
            repos={w.repos.data ?? []}
            loading={w.repos.isLoading}
            loadFailed={w.repos.isError}
            onRetry={() => w.repos.refetch()}
            repoId={w.repoId}
            gitlab={w.gitlab}
            onRepoChange={w.chooseRepo}
          />
        )}
        {step === 1 && (
          <PreviewStep
            loading={w.preview.isLoading}
            error={previewError}
            noTriggers={w.noTriggers}
            gitlabCi={w.gitlabCi}
            files={w.files}
            selected={w.selectedFile}
            edited={edited}
            onSelect={w.selectFile}
            onEdit={w.setEdited}
            onReset={w.resetEdited}
          />
        )}
        {step === 2 && (
          <ConfigureStep
            gitlab={w.gitlab}
            triggers={w.triggers}
            onToggleTrigger={w.toggleTrigger}
            postAs={w.postAs}
            onPostAsChange={w.setPostAs}
            runner={w.runner}
            onRunnerChange={w.setRunner}
            requirements={requirements}
            locked={edited}
            slug={w.slug}
          />
        )}
        {step === 3 && (
          <InstallStep
            method={w.method}
            onMethodChange={w.setMethod}
            repo={w.repo ?? ""}
            fileCount={w.files?.length ?? 0}
            slug={w.slug}
            pending={openPr.isPending}
            error={installError}
            prDone={installed}
            prUrl={openPr.data?.pr_url ?? null}
            gitlab={w.gitlab}
            gitlabCi={w.gitlabCi}
            zipName={w.zipName}
            requirements={requirements}
          />
        )}
      </Modal>
    </div>
  );
}
