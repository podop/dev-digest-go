/* HealthRing — a PR's health as a ring with the number inside. The colour follows the leaderboard
   bands (≥ 85 ok, ≥ 60 warn, else crit; FR21) — the vendored CircularScore uses 75 / 50 — and the
   number is always printed, so colour is never the only carrier (NFR4). */
import { healthColor } from "../../../../helpers";
import { s } from "./styles";

export interface HealthRingProps {
  health: number;
  size?: number;
  stroke?: number;
}

export function HealthRing({ health, size = 34, stroke = 3.5 }: HealthRingProps) {
  const r = (size - stroke) / 2;
  const circumference = 2 * Math.PI * r;
  const share = Math.min(100, Math.max(0, health)) / 100;
  return (
    <div style={s.ring(size)}>
      <svg aria-hidden width={size} height={size} style={s.svg}>
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--bg-hover)" strokeWidth={stroke} />
        <circle
          data-ring
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={healthColor(health)}
          strokeWidth={stroke}
          strokeDasharray={circumference}
          strokeDashoffset={circumference * (1 - share)}
          strokeLinecap="round"
        />
      </svg>
      <span className="tnum" style={s.value}>
        {health}
      </span>
    </div>
  );
}
