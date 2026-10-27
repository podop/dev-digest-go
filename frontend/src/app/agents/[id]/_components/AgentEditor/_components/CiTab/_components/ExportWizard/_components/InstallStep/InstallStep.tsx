/* InstallStep — finish by opening a PR (default) or by downloading the files as a zip.
   A successful PR shows its link and the next steps; the first PR run fails until the
   secrets and variables exist (listed from the export response), so those come first (EC17).
   GitLab (GitLab C FR43): a merge request, CI/CD variables, the include snippet when the project
   has a root configuration, and the "Pipelines must succeed" step. */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import type { CiExport } from "@devdigest/shared";
import type { InstallMethod } from "../../constants";
import { safeHttpUrl, zipFileName, type Requirements as RequirementsData, type WizardError } from "../../helpers";
import { IncludeSnippet } from "../IncludeSnippet";
import { Requirements } from "../Requirements";
import { WizardErrorBox } from "../WizardErrorBox";
import { s } from "./styles";

export function InstallStep({
  method,
  onMethodChange,
  repo,
  fileCount,
  slug,
  pending,
  error,
  prDone,
  prUrl,
  gitlab,
  gitlabCi,
  zipName,
  requirements,
}: {
  method: InstallMethod;
  onMethodChange: (method: InstallMethod) => void;
  repo: string;
  fileCount: number;
  slug: string | null;
  pending: boolean;
  error: WizardError | null;
  /** The PR request succeeded (its link may still be absent). */
  prDone: boolean;
  prUrl: string | null;
  /** The repository is on GitLab. */
  gitlab: boolean;
  /** The `gitlab_ci` block of the answer (what was committed); null for GitHub. */
  gitlabCi: CiExport["gitlab_ci"];
  /** Name of the zip downloaded last. */
  zipName: string | null;
  /** Secrets and variables the workflow needs, from the export response. */
  requirements: RequirementsData;
}) {
  const t = useTranslations("ci.exportWizard");
  const check = t("checkName", { slug: slug ?? "<slug>" });
  const href = safeHttpUrl(prUrl);
  const host = gitlab ? "gitlab." : "";
  const needsInclude = gitlab && gitlabCi != null && gitlabCi.root_config !== "create";

  if (prDone) {
    return (
      <div style={s.body}>
        <h3 style={s.title}>{t(`${host}successTitle`)}</h3>
        {href && (
          <a href={href} target="_blank" rel="noopener noreferrer" style={s.link}>
            {t(`${host}viewPr`)}
          </a>
        )}
        <p style={s.label}>{t("nextStepsTitle")}</p>
        {gitlab ? (
          <ol style={s.steps}>
            <li>{t("gitlab.nextAddVariables")}</li>
            {needsInclude && <li>{t("gitlab.nextInclude", { path: gitlabCi.config_path })}</li>}
            <li>{t("gitlab.nextMerge")}</li>
            <li>{t("gitlab.nextPipelinesMustSucceed")}</li>
          </ol>
        ) : (
          <ol style={s.steps}>
            <li>{t("nextAddSecrets")}</li>
            <li>{t("nextMerge")}</li>
            <li>{t("nextRequiredCheck", { check })}</li>
          </ol>
        )}
        <Requirements {...requirements} />
        {needsInclude && <IncludeSnippet configPath={gitlabCi.config_path} snippet={gitlabCi.include_snippet} />}
      </div>
    );
  }

  return (
    <fieldset style={s.body} disabled={pending}>
      <legend style={s.srOnly}>{t("installMethodLabel")}</legend>
      <label style={method === "pr" ? { ...s.option, ...s.optionOn } : s.option}>
        <input
          type="radio"
          name="ci-install-method"
          checked={method === "pr"}
          disabled={pending}
          onChange={() => onMethodChange("pr")}
        />
        <span style={s.optionText}>
          <span style={s.optionTitle}>
            {t(`${host}installCardTitle`)}
            <Badge color="var(--ok)" bg="var(--bg-hover)">
              {t("recommended")}
            </Badge>
          </span>
          <span style={s.muted}>{t(`${host}installCardBody`, { repo, count: fileCount })}</span>
        </span>
      </label>
      <label style={method === "zip" ? { ...s.option, ...s.optionOn } : s.option}>
        <input
          type="radio"
          name="ci-install-method"
          checked={method === "zip"}
          disabled={pending}
          onChange={() => onMethodChange("zip")}
        />
        <span style={s.optionText}>
          <span style={s.optionTitle}>{t("methodZip")}</span>
          <span style={s.muted}>{t(`${host}methodZipBody`, { name: zipFileName(slug ?? "agent") })}</span>
        </span>
      </label>

      <p style={s.muted}>{t(`${host}secretNote`)}</p>
      <Requirements {...requirements} />
      {pending && (
        <p role="status" style={s.muted}>
          {t("installing")}
        </p>
      )}
      {zipName && method === "zip" && (
        <p role="status" style={s.muted}>
          {t("zipDone", { name: zipName })}
        </p>
      )}
      {error && <WizardErrorBox error={error} />}
    </fieldset>
  );
}
