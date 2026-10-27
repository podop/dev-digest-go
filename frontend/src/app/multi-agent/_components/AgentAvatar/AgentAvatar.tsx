/* AgentAvatar — the agent's tinted icon tile (30×30) used on Configure run and the results columns. */
import { Icon } from "@devdigest/ui";
import type { AgentPersona } from "./constants";
import { s } from "./styles";

export function AgentAvatar({ persona, size = 30 }: { persona: AgentPersona; size?: number }) {
  const I = Icon[persona.icon];
  return (
    <span aria-hidden style={s.avatar(persona.color, size)}>
      <I size={16} />
    </span>
  );
}
