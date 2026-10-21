/* AgentGlyph — the agent's icon tile (agents carry no icon or colour of their own: Cpu on accent). */
import React from "react";
import { Icon } from "@devdigest/ui";
import { s } from "./styles";

export function AgentGlyph({ muted = false }: { muted?: boolean }) {
  return (
    <span aria-hidden style={muted ? { ...s.glyph, ...s.glyphMuted } : s.glyph}>
      <Icon.Cpu size={15} />
    </span>
  );
}
