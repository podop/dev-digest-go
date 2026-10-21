const PLACEHOLDER = "—";

/** Run duration in seconds as "12.3s" / "2m 05s"; "—" when unknown. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return PLACEHOLDER;
  if (seconds < 60) return `${Math.round(seconds * 10) / 10}s`;
  const whole = Math.round(seconds);
  return `${Math.floor(whole / 60)}m ${String(whole % 60).padStart(2, "0")}s`;
}
