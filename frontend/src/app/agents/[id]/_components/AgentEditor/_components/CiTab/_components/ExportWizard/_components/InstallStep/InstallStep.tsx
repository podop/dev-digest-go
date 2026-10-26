/* InstallStep — finish by opening a PR (default) or by downloading the files as a zip.
   A successful PR shows its link and the next steps; the first PR run fails until the
   secrets and variables exist (listed from the export response), so those come first (EC17). */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import type { InstallMethod } from "../../constants";
import { safeHttpUrl, zipFileName, type Requirements as RequirementsData, type WizardError } from "../../helpers";
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
  /** Name of the zip downloaded last. */
  zipName: string | null;
  /** Secrets and variables the workflow needs, from the export response. */
  requirements: RequirementsData;
}) {
  const t = useTranslations("ci.exportWizard");
  const check = t("checkName", { slug: slug ?? "<slug>" });
  const href = safeHttpUrl(prUrl);

  if (prDone) {
    return (
      <div style={s.body}>
        <h3 style={s.title}>{t("successTitle")}</h3>
        {href && (
          <a href={href} target="_blank" rel="noopener noreferrer" style={s.link}>
            {t("viewPr")}
          </a>
        )}
        <p style={s.label}>{t("nextStepsTitle")}</p>
        <ol style={s.steps}>
          <li>{t("nextAddSecrets")}</li>
          <li>{t("nextMerge")}</li>
          <li>{t("nextRequiredCheck", { check })}</li>
        </ol>
        <Requirements {...requirements} />
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
            {t("installCardTitle")}
            <Badge color="var(--ok)" bg="var(--bg-hover)">
              {t("recommended")}
            </Badge>
          </span>
          <span style={s.muted}>{t("installCardBody", { repo, count: fileCount })}</span>
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
          <span style={s.muted}>{t("methodZipBody", { name: zipFileName(slug ?? "agent") })}</span>
        </span>
      </label>

      <p style={s.muted}>{t("secretNote")}</p>
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
