/* AgentPickCard — one selectable agent on Configure run: checkbox in the agent's colour, icon
   tile, name, description and "time · cost" estimate (Claude Design PersonaPickCard; the name
   is set to --text-primary — the prototype left it to the button's UA colour, dark on dark). */
"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { Agent } from "@devdigest/shared";
import { AgentAvatar, agentPersona } from "@/app/multi-agent/_components/AgentAvatar";
import { s } from "./styles";

export function AgentPickCard({
  agent,
  on,
  onToggle,
  estimate,
}: {
  agent: Pick<Agent, "id" | "name" | "description" | "enabled">;
  on: boolean;
  onToggle: () => void;
  /** "8.2s · $0.06", or "—" without history. */
  estimate: string;
}) {
  const t = useTranslations("runs.multiAgent.configure");
  const persona = agentPersona(agent.name, agent.id);
  const name = agent.enabled ? agent.name : `${agent.name} · ${t("agentDisabled")}`;
  return (
    // biome-ignore lint/a11y/useSemanticElements: the whole card is the checkbox target (prototype PersonaPickCard)
    <button type="button" role="checkbox" aria-checked={on} onClick={onToggle} style={s.card(on, persona.color)}>
      <span aria-hidden style={s.box(on, persona.color)}>
        {on && <Icon.Check size={12} style={s.check} />}
      </span>
      <AgentAvatar persona={persona} />
      <span style={s.main}>
        <span style={s.name}>{name}</span>
        {agent.description && <span style={s.desc}>{agent.description}</span>}
      </span>
      <span className="mono" style={s.estimate}>
        {estimate}
      </span>
    </button>
  );
}
