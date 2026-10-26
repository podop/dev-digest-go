/* ImportProblem — the inline error of the import card (EC14, 413/415/422, 5xx). The preview
   and install hooks are quiet for every code, so this is the only place a failure shows. */
"use client";

import { useTranslations } from "next-intl";
import type { ImportProblem as Problem } from "../../helpers";
import { s } from "./styles";

export function ImportProblem({ problem }: { problem: Problem }) {
  const t = useTranslations("integrationsImport.problem");
  // A conflict is listed by the preview itself, next to the disabled Install button.
  if (problem.kind === "conflict") return null;

  return (
    <div role="alert" style={s.box}>
      <div style={s.message}>
        {problem.kind === "type" && t("wrongType")}
        {problem.kind === "size" && t("tooLarge", { max: problem.maxMb })}
        {problem.kind === "message" && problem.message}
        {problem.kind === "failed" && t("failed")}
      </div>
      {problem.kind === "message" && problem.issues.length > 0 && (
        <ul style={s.issues} aria-label={t("issuesHeading")}>
          {[...new Set(problem.issues)].map((issue) => (
            <li key={issue}>{issue}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
