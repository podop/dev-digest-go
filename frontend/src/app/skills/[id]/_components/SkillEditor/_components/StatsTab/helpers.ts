/** Whether the skill was never in a run's prompt within the window. */
export function neverAttached(runsAttached: number): boolean {
  return runsAttached === 0;
}
