/* FirstTasks — body of the "First tasks" section: up to three cards with the title, the path
   (monospace, truncated with a tooltip; a folder is allowed) and a complexity badge.
   Empty → "Not enough information". */
"use client";

import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import type { OnboardingFirstTask } from "@devdigest/shared";
import { COMPLEXITY_COLOR } from "./constants";
import { s } from "./styles";

export function FirstTasks({ tasks }: { tasks: readonly OnboardingFirstTask[] }) {
  const t = useTranslations("onboarding");
  if (tasks.length === 0) return <p style={s.empty}>{t("empty")}</p>;

  return (
    <ul aria-label={t("tasks.list")} style={s.grid}>
      {tasks.map((task) => {
        const c = COMPLEXITY_COLOR[task.complexity];
        return (
          <li key={`${task.path}:${task.title}`} style={s.card}>
            <span style={s.title}>{task.title}</span>
            <span className="mono" title={task.path} style={s.path}>
              {task.path}
            </span>
            <span style={s.badge}>
              <Badge color={c} bg="transparent" style={{ border: `1px solid ${c}` }}>
                {t(`tasks.complexity.${task.complexity}`)}
              </Badge>
            </span>
          </li>
        );
      })}
    </ul>
  );
}
