import type { Agent } from "@devdigest/shared";

/** Outcome of clicking one chip. */
export type ToggleResult =
  | { kind: "changed"; ids: string[] }
  | { kind: "limit" }
  | { kind: "ignored" };

/** Ids of `selected` that still name an agent; ids of deleted agents are dropped (EC10). */
export function liveSelection(agents: readonly Agent[], selected: readonly string[]): Set<string> {
  const known = new Set(agents.map((a) => a.id));
  return new Set(selected.filter((id) => known.has(id)));
}

/**
 * The selection after a click on agent `id`, in agents-list order, without ids of deleted
 * agents. A selected agent can always be deselected (A7, also a disabled one); an unselected
 * disabled agent cannot be selected; selecting past `max` is refused (EC19).
 */
export function toggleAgent(
  agents: readonly Agent[],
  selected: readonly string[],
  id: string,
  max: number,
): ToggleResult {
  const agent = agents.find((a) => a.id === id);
  if (!agent) return { kind: "ignored" };
  const live = liveSelection(agents, selected);
  if (live.has(id)) {
    live.delete(id);
  } else {
    if (!agent.enabled) return { kind: "ignored" };
    if (live.size >= max) return { kind: "limit" };
    live.add(id);
  }
  return { kind: "changed", ids: agents.filter((a) => live.has(a.id)).map((a) => a.id) };
}
