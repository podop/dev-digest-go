/* HostAgentPicker — the "Run with" control of the skill Evals tab: the agent whose prompt and
   model a skill run uses. Controlled; the tab derives the default and owns the pick. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { Agent } from "@devdigest/shared";
import { Select } from "@/components/select";
import { s } from "./styles";

export function HostAgentPicker({
  agents,
  value,
  onChange,
  disabled,
}: {
  agents: readonly Agent[];
  value: string | null;
  onChange: (agentId: string) => void;
  disabled?: boolean;
}) {
  const t = useTranslations("eval.skillEvals");
  const options = React.useMemo(() => agents.map((a) => ({ value: a.id, label: a.name })), [agents]);
  return (
    <div style={s.wrap}>
      <span style={s.label} aria-hidden="true">
        {t("runWith")}
      </span>
      <span style={s.select}>
        <Select value={value ?? ""} onChange={onChange} options={options} mono={false} aria-label={t("runWith")} disabled={disabled || agents.length === 0} />
      </span>
    </div>
  );
}
