import type { IconName } from "@devdigest/ui";

/** Colour + icon an agent is drawn with (Claude Design screen_multiagent.jsx personas). */
export interface AgentPersona {
  /** 6-digit hex, so alpha suffixes ("1f", "12") make the tinted backgrounds. */
  color: string;
  icon: IconName;
}

/** Name keyword → persona, first match wins; colours and icons of the prototype's personas. */
export const PERSONA_RULES: readonly { match: RegExp; persona: AgentPersona }[] = [
  { match: /secur/i, persona: { color: "#ef4444", icon: "Shield" } },
  { match: /perf/i, persona: { color: "#f59e0b", icon: "Zap" } },
  { match: /test|\bqa\b|quality/i, persona: { color: "#14b8a6", icon: "FlaskConical" } },
  { match: /archit/i, persona: { color: "#10b981", icon: "Boxes" } },
  { match: /custom|client|\buser|\bux\b|\bdx\b/i, persona: { color: "#8b5cf6", icon: "Users" } },
  { match: /mentor|junior|readab|style|general/i, persona: { color: "#3b82f6", icon: "Lightbulb" } },
];

/** Fallback colours (the prototype's Avatar hues), picked by a stable hash of the agent. */
export const FALLBACK_COLORS: readonly string[] = ["#3b82f6", "#10b981", "#f59e0b", "#8b5cf6", "#ec4899", "#14b8a6"];

export const FALLBACK_ICON: IconName = "Cpu";
