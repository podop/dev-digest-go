"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { Field } from "@/components/settings-kit";
// Zod-free subpath: the barrel would pull zod + every schema into /settings.
import { AUTO_REVIEW_MAX_AGENTS } from "@devdigest/shared/constants/settings";
import type { Agent } from "@devdigest/shared";
import { liveSelection, toggleAgent } from "./helpers";
import { s } from "./styles";

interface AgentChipsProps {
  /** All agents of the workspace, in the agents list order. */
  agents: Agent[];
  /** Stored `automatic_reviews.agent_ids`; ids of deleted agents are ignored. */
  selected: string[];
  /** True while a save is in flight. */
  disabled?: boolean;
  /** The new `agent_ids` (agents-list order, deleted ids dropped) after a click. */
  onChange: (ids: string[]) => void;
}

/**
 * Automatic Reviews → "Agents to run" (FR15): one toggle chip per agent. Controlled: the
 * section owns the saved value and the PUT. A selected disabled agent shows "skipped — disabled"
 * and can be deselected (A7); an unselected disabled agent cannot be selected; at most
 * `AUTO_REVIEW_MAX_AGENTS` can be selected (EC19).
 */
export function AgentChips({ agents, selected, disabled = false, onChange }: AgentChipsProps) {
  const t = useTranslations("autoReviewAgents");
  const [limitHit, setLimitHit] = React.useState(false);
  const live = liveSelection(agents, selected);

  const click = (id: string) => {
    const result = toggleAgent(agents, selected, id, AUTO_REVIEW_MAX_AGENTS);
    setLimitHit(result.kind === "limit");
    if (result.kind === "changed") onChange(result.ids);
  };

  return (
    <Field label={t("label")} hint={t("hint")}>
      {agents.length === 0 ? (
        <div style={s.empty}>
          <span>{t("empty")}</span>
          <Link href="/agents" style={s.link}>
            {t("create")}
          </Link>
        </div>
      ) : (
        <fieldset aria-label={t("groupAria")} style={s.list}>
          {agents.map((agent) => {
            const on = live.has(agent.id);
            const blocked = !agent.enabled && !on;
            return (
              <button
                key={agent.id}
                type="button"
                aria-pressed={on}
                disabled={disabled || blocked}
                onClick={() => click(agent.id)}
                style={{
                  ...s.chip,
                  ...(on ? s.chipOn : null),
                  ...(agent.enabled ? null : s.chipMuted),
                  ...(disabled || blocked ? s.chipBlocked : null),
                }}
              >
                {on ? <Icon.Check size={13} aria-hidden /> : <Icon.Plus size={13} aria-hidden />}
                {agent.name}
                {!agent.enabled && on && <span style={s.skipped}>{t("skippedDisabled")}</span>}
              </button>
            );
          })}
        </fieldset>
      )}
      {limitHit && (
        <div role="alert" style={s.notice}>
          {t("limit", { max: AUTO_REVIEW_MAX_AGENTS })}
        </div>
      )}
    </Field>
  );
}
