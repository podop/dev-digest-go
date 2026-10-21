import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";
import { EVAL_METRICS } from "@/components/eval-metrics";
import { AXIS_DIVISIONS, AXIS_FLOOR, CHART, LONE_COLUMN_WIDTH } from "./constants";

export interface ChartPoint {
  x: number;
  y: number;
  run: EvalRunSummary;
}

const PLOT_WIDTH = CHART.width - CHART.left - CHART.right;
const PLOT_HEIGHT = CHART.height - CHART.top - CHART.bottom;

/** Bottom of the y axis (FR6): 0.6 when every plotted value is at or above it, else the
 *  largest multiple of 0.1 not above the lowest value (never below 0). */
export function axisBottom(runs: readonly EvalRunSummary[]): number {
  const values = runs.flatMap((run) => EVAL_METRICS.flatMap((m) => (run[m] == null ? [] : [run[m]])));
  const lowest = Math.min(...values);
  if (values.length === 0 || lowest >= AXIS_FLOOR) return AXIS_FLOOR;
  // Round off float noise (0.29 * 100 style products) before flooring to a tenth.
  return Math.max(0, Math.floor(Number((lowest * AXIS_DIVISIONS).toFixed(6)))) / AXIS_DIVISIONS;
}

/** Gridline values from 1.0 down to `bottom`, every 0.1 (one-decimal precision). */
export function axisTicks(bottom: number): number[] {
  const steps = Math.round((1 - bottom) * AXIS_DIVISIONS);
  return Array.from({ length: steps + 1 }, (_, i) => (AXIS_DIVISIONS - i) / AXIS_DIVISIONS);
}

/** X of run `index` of `count`, equally spaced (a lone run sits in the middle). */
export function xOf(index: number, count: number): number {
  return CHART.left + (count <= 1 ? PLOT_WIDTH / 2 : (index / (count - 1)) * PLOT_WIDTH);
}

/** Y of a 0–1 ratio: 1 at the top of the plot, `bottom` at its foot. */
export function yOf(ratio: number, bottom: number): number {
  return CHART.top + ((1 - ratio) / (1 - bottom)) * PLOT_HEIGHT;
}

function pointOf(runs: readonly EvalRunSummary[], index: number, metric: EvalMetricName, bottom: number): ChartPoint | null {
  const run = runs[index]!;
  const value = run[metric];
  return value == null ? null : { x: xOf(index, runs.length), y: yOf(value, bottom), run };
}

/** Points of one metric over `runs` (oldest first); runs where it is undefined leave a gap. */
export function metricPoints(runs: readonly EvalRunSummary[], metric: EvalMetricName, bottom: number): ChartPoint[] {
  return runs.flatMap((_, i) => pointOf(runs, i, metric, bottom) ?? []);
}

/** Lines of consecutive defined runs: split at every gap, a single point draws no line. */
export function lineSegments(runs: readonly EvalRunSummary[], metric: EvalMetricName, bottom: number): ChartPoint[][] {
  const segments: ChartPoint[][] = [];
  let current: ChartPoint[] = [];
  runs.forEach((_, i) => {
    const point = pointOf(runs, i, metric, bottom);
    if (point) {
      current.push(point);
      return;
    }
    if (current.length) segments.push(current);
    current = [];
  });
  if (current.length) segments.push(current);
  return segments.filter((seg) => seg.length >= 2);
}

export function toPolyline(points: readonly ChartPoint[]): string {
  return points.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" ");
}

/** The point of a series' newest defined run — the only one drawn as a dot at rest (FR7). */
export function newestPoint(points: readonly ChartPoint[]): ChartPoint | null {
  return points[points.length - 1] ?? null;
}

/** Hover column of run `index` in viewBox px: half a step either side, kept inside the chart. */
export function columnOf(index: number, count: number): { left: number; width: number } {
  const x = xOf(index, count);
  const half = count <= 1 ? LONE_COLUMN_WIDTH / 2 : PLOT_WIDTH / (count - 1) / 2;
  const left = Math.max(0, x - half);
  return { left, width: Math.min(CHART.width, x + half) - left };
}

/** Whether the tooltip opens to the left of the guide (runs in the right half of the plot). */
export function tooltipOnLeft(index: number, count: number): boolean {
  return xOf(index, count) > CHART.width / 2;
}
