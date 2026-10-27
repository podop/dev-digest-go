import { FALLBACK_COLORS, FALLBACK_ICON, PERSONA_RULES, type AgentPersona } from "./constants";

/** Persona of an agent: by a keyword of its name, otherwise a colour hashed from its id (or name). */
export function agentPersona(name: string, id?: string | null): AgentPersona {
  const rule = PERSONA_RULES.find((r) => r.match.test(name));
  if (rule) return rule.persona;
  const key = id || name;
  let hash = 0;
  for (const ch of key) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
  return { color: FALLBACK_COLORS[hash % FALLBACK_COLORS.length] ?? "#3b82f6", icon: FALLBACK_ICON };
}
