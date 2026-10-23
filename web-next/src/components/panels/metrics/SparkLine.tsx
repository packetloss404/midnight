// SparkLine — dependency-free inline-SVG time-series line for the metrics
// tiles. Draws a single series with an optional area fill using the shared
// chart tokens (`--chart-*`), so it tracks light/dark automatically.
//
// Deliberately minimal: no axes chrome, a faint baseline grid, an accessible
// <title>, and value/time endpoints described in the aria-label. The panel
// owns labels and units around it.

import { useId } from "react";

export type SparkPoint = { t: number; v: number };

type Props = {
  points: SparkPoint[];
  /** Chart token index (1–4) → `var(--chart-N)`. Default 1 (accent). */
  colorIndex?: 1 | 2 | 3 | 4;
  width?: number;
  height?: number;
  /** Force the y-domain floor to 0 (good for counts). Default true. */
  zeroBaseline?: boolean;
  label: string;
  /** Optional formatter for the aria value readout. */
  formatValue?: (v: number) => string;
};

export function SparkLine({
  points,
  colorIndex = 1,
  width = 240,
  height = 56,
  zeroBaseline = true,
  label,
  formatValue = (v) => String(v),
}: Props) {
  const gradId = useId();
  const stroke = `var(--chart-${colorIndex})`;

  if (points.length === 0) {
    return (
      <svg
        viewBox={`0 0 ${width} ${height}`}
        width="100%"
        height={height}
        role="img"
        aria-label={`${label}: no samples yet`}
        preserveAspectRatio="none"
      >
        <line
          x1="0"
          y1={height - 1}
          x2={width}
          y2={height - 1}
          stroke="var(--chart-grid)"
          strokeWidth="1"
        />
      </svg>
    );
  }

  const pad = 3;
  const w = width;
  const h = height;
  const values = points.map((p) => p.v);
  const min = zeroBaseline ? 0 : Math.min(...values);
  const maxRaw = Math.max(...values);
  const max = maxRaw === min ? min + 1 : maxRaw;

  const ts = points.map((p) => p.t);
  const tMin = Math.min(...ts);
  const tMax = Math.max(...ts);
  const tSpan = tMax === tMin ? 1 : tMax - tMin;

  const x = (t: number) => pad + ((t - tMin) / tSpan) * (w - pad * 2);
  const y = (v: number) => h - pad - ((v - min) / (max - min)) * (h - pad * 2);

  // Single-sample series: draw a flat marker line so it reads as "steady".
  const coords = points.map((p) => ({ px: x(p.t), py: y(p.v) }));
  const line = coords.map((c, i) => `${i === 0 ? "M" : "L"}${c.px.toFixed(1)},${c.py.toFixed(1)}`).join(" ");
  const area =
    `${line} L${coords[coords.length - 1].px.toFixed(1)},${(h - pad).toFixed(1)}` +
    ` L${coords[0].px.toFixed(1)},${(h - pad).toFixed(1)} Z`;

  const first = points[0];
  const last = points[points.length - 1];
  const ariaLabel =
    `${label}: ${formatValue(last.v)} now, ${formatValue(first.v)} at series start, ` +
    `${points.length} sample${points.length === 1 ? "" : "s"}`;

  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      width="100%"
      height={h}
      role="img"
      aria-label={ariaLabel}
      preserveAspectRatio="none"
    >
      <title>{ariaLabel}</title>
      <defs>
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={stroke} stopOpacity="0.28" />
          <stop offset="100%" stopColor={stroke} stopOpacity="0" />
        </linearGradient>
      </defs>
      <line
        x1="0"
        y1={h - 1}
        x2={w}
        y2={h - 1}
        stroke="var(--chart-grid)"
        strokeWidth="1"
      />
      <path d={area} fill={`url(#${gradId})`} stroke="none" />
      <path
        d={line}
        fill="none"
        stroke={stroke}
        strokeWidth="1.75"
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
      <circle cx={coords[coords.length - 1].px} cy={coords[coords.length - 1].py} r="2.5" fill={stroke} />
    </svg>
  );
}
