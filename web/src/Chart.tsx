import { useEffect, useRef, useState } from 'react'
import { formatBytes } from './lib'

export type Point = { t: number; v: number } // t in Unix seconds

/** A line on a chart. Series in one chart share their sample times. */
export type Series = { label: string; points: Point[] }

const PAD = { top: 8, right: 8, bottom: 20, left: 48 }
const colors = ['var(--chart)', 'var(--chart-2)', 'var(--chart-3)']

/** Rounds up to a round number whose half is round too, for the middle tick. */
function niceCeil(x: number): number {
  if (x <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(x))
  for (const m of [1, 2, 3, 4, 5, 6, 8, 10]) if (m * p >= x - 1e-9) return m * p
  return 10 * p
}

/** niceCeil in the unit formatBytes will show, so ticks read 2 MB, not 1.9 MB. */
function niceCeilBytes(x: number): number {
  if (x <= 0) return 1024
  const unit = 1024 ** Math.max(0, Math.floor(Math.log(x) / Math.log(1024)))
  const n = niceCeil(x / unit)
  return n >= 1000 ? 1024 * unit : n * unit // 1 MB, not 1000 KB
}

const clock = (t: number) => new Date(t * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })

/**
 * Lines over time: 2px strokes, hairline gridlines, and a crosshair that
 * snaps to the nearest sample with a tooltip of every series. One series
 * gets a 10% wash under it; two or three get a legend. An optional
 * reference line marks a limit.
 */
export function TimeChart({
  series,
  format,
  label,
  height = 140,
  reference,
  bytes = false,
  floor = 0,
}: {
  series: Series[]
  format: (v: number) => string
  label: string
  height?: number
  reference?: { value: number; label: string }
  /** Ticks at round numbers of bytes rather than of ten. */
  bytes?: boolean
  /** The least the scale goes up to, so an idle series isn't magnified into noise. */
  floor?: number
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(0)
  const [hover, setHover] = useState<number | null>(null)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setWidth(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const times = series[0]?.points ?? []
  const multi = series.length > 1
  const legend = multi && (
    <div className="chart-legend">
      {series.map((s, i) => (
        <span key={s.label}>
          <i style={{ background: colors[i] }} />
          {s.label}
        </span>
      ))}
    </div>
  )

  if (times.length < 2) {
    return (
      <div ref={ref}>
        {legend}
        <div className="chart-empty" style={{ height }}>
          Collecting samples (one every 15 seconds)…
        </div>
      </div>
    )
  }

  const t0 = times[0].t
  const t1 = times[times.length - 1].t
  const dataPeak = Math.max(...series.flatMap((s) => s.points.map((p) => p.v)))
  // A limit line keeps some headroom above it, so its label isn't on the edge.
  const peak = Math.max(dataPeak * 1.1, (reference?.value ?? 0) * 1.15, floor)
  const max = bytes ? niceCeilBytes(peak) : niceCeil(peak)
  const w = Math.max(width - PAD.left - PAD.right, 10)
  const h = height - PAD.top - PAD.bottom
  const x = (t: number) => PAD.left + ((t - t0) / Math.max(t1 - t0, 1)) * w
  const y = (v: number) => PAD.top + h - (Math.min(v, max) / max) * h
  const path = (pts: Point[]) => pts.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join('')
  const ticks = [0, max / 2, max]

  function onMove(e: React.PointerEvent<SVGRectElement>) {
    const box = e.currentTarget.getBoundingClientRect()
    const tx = t0 + ((e.clientX - box.left) / box.width) * (t1 - t0)
    let best = 0
    for (let i = 1; i < times.length; i++) {
      if (Math.abs(times[i].t - tx) < Math.abs(times[best].t - tx)) best = i
    }
    setHover(best)
  }

  const ht = hover !== null ? times[hover]?.t : undefined
  const summary = series
    .map((s) => {
      const vs = s.points.map((p) => p.v)
      return `${multi ? s.label + ' ' : ''}now ${format(vs[vs.length - 1])}, highest ${format(Math.max(...vs))}`
    })
    .join('; ')
  return (
    <div ref={ref} className="chart">
      {legend}
      <svg height={height} role="img" aria-label={`${label} since ${clock(t0)}: ${summary}`}>
        <g className="grid">
          {ticks.map((v) => (
            <line key={v} x1={PAD.left} x2={PAD.left + w} y1={y(v)} y2={y(v)} />
          ))}
        </g>
        <g className="axis">
          {ticks.map((v) => (
            <text key={v} x={PAD.left - 6} y={y(v) + 3.5} textAnchor="end">
              {format(v)}
            </text>
          ))}
          <text x={PAD.left} y={height - 4}>
            {clock(t0)}
          </text>
          <text x={PAD.left + w} y={height - 4} textAnchor="end">
            {clock(t1)}
          </text>
        </g>
        {!multi && (
          <path
            className="series-area"
            d={`${path(times)}L${x(t1).toFixed(1)},${y(0)}L${x(t0).toFixed(1)},${y(0)}Z`}
          />
        )}
        {series.map((s, i) => (
          <path key={s.label} className="series-line" style={{ stroke: colors[i] }} d={path(s.points)} />
        ))}
        {reference && reference.value <= max && (
          <g>
            <line className="ref-line" x1={PAD.left} x2={PAD.left + w} y1={y(reference.value)} y2={y(reference.value)} />
            <text className="ref-label" x={PAD.left + w} y={y(reference.value) - 4} textAnchor="end">
              {reference.label}
            </text>
          </g>
        )}
        {ht !== undefined && (
          <g>
            <line className="crosshair" x1={x(ht)} x2={x(ht)} y1={PAD.top} y2={PAD.top + h} />
            {series.map((s, i) => {
              const p = s.points[hover!]
              return p && <circle key={s.label} className="marker" style={{ fill: colors[i] }} cx={x(p.t)} cy={y(p.v)} r={4} />
            })}
          </g>
        )}
        <rect
          x={PAD.left}
          y={PAD.top}
          width={w}
          height={h}
          fill="transparent"
          onPointerMove={onMove}
          onPointerLeave={() => setHover(null)}
        />
      </svg>
      {ht !== undefined && (
        <div className="chart-tip" style={{ left: Math.min(Math.max(x(ht), 70), width - 70), top: multi ? 22 : 0 }}>
          {series.map((s, i) => {
            const p = s.points[hover!]
            return (
              p && (
                <div key={s.label} className="chart-tip-row">
                  {multi && <i style={{ background: colors[i] }} />}
                  {multi && <span>{s.label}</span>}
                  <strong>{format(p.v)}</strong>
                </div>
              )
            )
          })}
          <span className="muted">{clock(ht)}</span>
        </div>
      )}
    </div>
  )
}

/** A single series with a wash under it. */
export function AreaChart({
  points,
  label,
  ...rest
}: { points: Point[]; label: string } & Omit<Parameters<typeof TimeChart>[0], 'series' | 'label'>) {
  return <TimeChart series={[{ label, points }]} label={label} {...rest} />
}

/** Percent with a decimal below 10, so small values stay distinct; under 0.05 is 0%. */
export function percent(v: number): string {
  if (v < 0.05) return '0%'
  return v >= 10 || Number.isInteger(v) ? `${Math.round(v)}%` : `${v.toFixed(1)}%`
}

/** Bytes, with 0 for nothing rather than a dash. */
export function bytes(v: number): string {
  return v ? formatBytes(v) : '0'
}

export function bytesPerSecond(v: number): string {
  return v ? `${formatBytes(v)}/s` : '0'
}

export type Range = '1h' | '6h' | '24h'

/** The chart range control: Sideyard keeps a day of samples. */
export function RangePicker({ value, onChange }: { value: Range; onChange: (r: Range) => void }) {
  return (
    <div className="segmented" role="group" aria-label="Chart range">
      {(['1h', '6h', '24h'] as Range[]).map((r) => (
        <button key={r} className={r === value ? 'on' : ''} aria-pressed={r === value} onClick={() => onChange(r)}>
          {r === '1h' ? 'Last hour' : r === '6h' ? '6 hours' : '24 hours'}
        </button>
      ))}
    </div>
  )
}

/** Shown instead of charts where the server can't report usage (not Linux). */
export function NoMetrics() {
  return (
    <div className="card chart-card">
      <div className="chart-empty" style={{ height: 'auto', padding: '18px 0' }}>
        CPU and memory figures come from Linux; this machine doesn't report them.
      </div>
    </div>
  )
}
