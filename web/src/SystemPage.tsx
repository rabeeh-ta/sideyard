import { useState } from 'react'
import { ArrowUpCircle } from 'lucide-react'
import { api, type SystemInfo } from './api'
import { bytes, bytesPerSecond, NoMetrics, percent, RangePicker, TimeChart, type Range } from './Chart'
import { formatBytes, formatDate, timeAgo, uptime, usePoll } from './lib'
import { PageHead, Stat, StatusBadge } from './ui'

/** The machine Sideyard runs on: usage now and over the last day, what uses it, and what it is. */
export function SystemPage() {
  const [range, setRange] = useState<Range>('1h')
  const { data: sys, error } = usePoll(api.system, 10000, 'system')
  const { data: metrics } = usePoll(() => api.serverMetrics(range), 15000, `system-metrics:${range}`)
  const { data: update } = usePoll(api.update, 60000, 'system-update')

  if (error) return <p className="error">{error}</p>
  if (!sys) return null
  const { host, now, sideyard } = sys
  const linux = now.memTotalBytes > 0
  const pts = metrics ?? []
  const series = (label: string, pick: (m: (typeof pts)[number]) => number) => ({
    label,
    points: pts.map((m) => ({ t: m.t, v: pick(m) })),
  })
  const used = now.memTotalBytes - now.memAvailableBytes
  const data = sys.mounts.find((m) => m.data)
  const swapUsed = now.swapTotalBytes - now.swapFreeBytes

  return (
    <div className="page">
      <PageHead
        title="System"
        description={[host.hostname, host.os, host.bootedAt && `up ${uptime(host.bootedAt)}`].filter(Boolean).join(' · ')}
      >
        {linux && <RangePicker value={range} onChange={setRange} />}
      </PageHead>

      {update?.available && (
        <a className="banner banner-link" href="#/settings/updates">
          <ArrowUpCircle size={16} />
          Sideyard {update.latest} is available (this is {update.current}).
          <span className="grow" />
          <span className="link">Update</span>
        </a>
      )}

      {!linux && (
        <div style={{ marginBottom: 16 }}>
          <NoMetrics />
        </div>
      )}
      {linux && (
        <>
          <div className="stats">
            <Stat
              label="CPU"
              value={percent(now.cpuPercent)}
              meter={now.cpuPercent / 100}
              note={`${host.cpus} cores · load ${now.load1.toFixed(2)} ${now.load5.toFixed(2)} ${now.load15.toFixed(2)}`}
            />
            <Stat
              label="Memory"
              value={`${formatBytes(used)} of ${formatBytes(now.memTotalBytes)}`}
              meter={used / now.memTotalBytes}
              note={
                now.swapTotalBytes > 0
                  ? `swap ${bytes(swapUsed)} of ${formatBytes(now.swapTotalBytes)}`
                  : `${formatBytes(now.memCachedBytes)} cache, reclaimable`
              }
            />
            {data && (
              <Stat
                label={`Disk · ${data.path}`}
                value={`${formatBytes(data.usedBytes)} of ${formatBytes(data.totalBytes)}`}
                meter={data.usedBytes / (data.usedBytes + data.availBytes)}
                note={`${formatBytes(data.availBytes)} free · Sideyard's data is here`}
              />
            )}
            <Stat label="Network" value={`${bytesPerSecond(now.netRxBps)} in`} note={`${bytesPerSecond(now.netTxBps)} out`} />
          </div>

          <MemoryBar sys={sys} />

          <div className="card chart-card" style={{ marginBottom: 12 }}>
            <div className="chart-head">
              <span className="muted">CPU, percent of all {host.cpus} cores</span>
              <strong>{percent(now.cpuPercent)}</strong>
            </div>
            <TimeChart
              series={[series('Busy', (m) => m.cpu), series('Waiting on disk', (m) => m.iowait), series('Stolen by the host', (m) => m.steal)]}
              label="CPU"
              format={percent}
              height={170}
              floor={10}
            />
          </div>
          <div className="charts">
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">Memory in use</span>
                <strong>{formatBytes(used)}</strong>
              </div>
              <TimeChart
                series={[series('In use', (m) => m.mem)]}
                label="Memory in use"
                format={bytes}
                bytes
                reference={{ value: now.memTotalBytes, label: `total ${formatBytes(now.memTotalBytes)}` }}
              />
            </div>
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">Load average, 1 minute</span>
                <strong>{now.load1.toFixed(2)}</strong>
              </div>
              <TimeChart
                series={[series('Load', (m) => m.load1)]}
                label="Load average"
                format={(v) => (v >= 10 ? v.toFixed(0) : v.toFixed(1))}
                floor={1}
                reference={{ value: host.cpus, label: `${host.cpus} cores` }}
              />
            </div>
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">Disk I/O</span>
                <strong>{bytesPerSecond(now.diskReadBps + now.diskWriteBps)}</strong>
              </div>
              <TimeChart
                series={[series('Read', (m) => m.diskRead), series('Written', (m) => m.diskWrite)]}
                label="Disk I/O per second"
                format={bytesPerSecond}
                bytes
                floor={64 * 1024}
              />
            </div>
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">Network</span>
                <strong>{bytesPerSecond(now.netRxBps + now.netTxBps)}</strong>
              </div>
              <TimeChart
                series={[series('In', (m) => m.netRx), series('Out', (m) => m.netTx)]}
                label="Network traffic per second"
                format={bytesPerSecond}
                bytes
                floor={64 * 1024}
              />
            </div>
          </div>
        </>
      )}

      <AppsTable sys={sys} />

      <div className="card section">
        <h3>Disks</h3>
        <div className="scroll-x">
          <table className="table">
            <thead>
              <tr>
                <th>Mounted at</th>
                <th>Device</th>
                <th>Type</th>
                <th style={{ width: '34%' }}>Used</th>
                <th className="num">Free</th>
              </tr>
            </thead>
            <tbody>
              {sys.mounts.map((m) => {
                const share = m.usedBytes / Math.max(m.usedBytes + m.availBytes, 1)
                return (
                  <tr key={m.path}>
                    <td>
                      <code>{m.path}</code> {m.data && <span className="badge tone-green">Sideyard data</span>}
                    </td>
                    <td className="muted">{m.device || '—'}</td>
                    <td className="muted">{m.fsType || '—'}</td>
                    <td>
                      <div className="meter-cell">
                        <div className={`progress ${share >= 0.9 ? 'high' : ''}`}>
                          <div style={{ width: `${share * 100}%` }} />
                        </div>
                        <span className="small-text muted">
                          {formatBytes(m.usedBytes)} of {formatBytes(m.totalBytes)} · {Math.round(share * 100)}%
                        </span>
                      </div>
                    </td>
                    <td className="num">{formatBytes(m.availBytes)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card section">
        <h3>Machine</h3>
        <dl className="facts">
          {(
            [
              ['Hostname', host.hostname],
              ['Operating system', host.os],
              ['Kernel', host.kernel],
              ['Hardware', host.machine],
              ['CPU', [host.cpuModel, `${host.cpus} cores`, host.arch].filter(Boolean).join(' · ')],
              ['Booted', host.bootedAt && `${formatDate(host.bootedAt)} (${timeAgo(host.bootedAt)})`],
              ['Processes', linux ? String(now.processes) : ''],
              ['Sideyard', `${sideyard.version} · ${sideyard.runner} runner · started ${timeAgo(sideyard.startedAt)}`],
              ['Instance', sideyard.instance],
              ['Data directory', sideyard.dataDir],
            ] as [string, string | undefined][]
          )
            .filter(([, v]) => v)
            .map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </div>
            ))}
        </dl>
      </div>
    </div>
  )
}

/** How the server's memory divides between the apps, Sideyard, everything else, and what's available. */
function MemoryBar({ sys }: { sys: SystemInfo }) {
  const { now, sideyard } = sys
  const total = now.memTotalBytes
  const used = total - now.memAvailableBytes
  const apps = sys.apps.reduce((n, a) => n + a.memoryBytes, 0)
  const own = Math.min(sideyard.memoryBytes, Math.max(used - apps, 0))
  const other = Math.max(used - apps - own, 0)
  const parts = [
    { label: 'Apps', value: Math.min(apps, total), color: 'var(--chart)' },
    { label: 'Sideyard', value: own, color: 'var(--chart-2)' },
    { label: 'Other processes', value: other, color: 'var(--chart-3)' },
  ]
  return (
    <div className="card section memory-bar">
      <div className="chart-head">
        <span className="muted">Memory by user</span>
        <span className="small-text muted">{formatBytes(now.memAvailableBytes)} available</span>
      </div>
      <div className="stacked" role="img" aria-label={parts.map((p) => `${p.label} ${bytes(p.value)}`).join(', ') + `, of ${formatBytes(total)}`}>
        {parts.map(
          (p) =>
            p.value > 0 && (
              <div key={p.label} style={{ width: `${(p.value / total) * 100}%`, background: p.color }} title={`${p.label}: ${bytes(p.value)}`} />
            ),
        )}
      </div>
      <div className="chart-legend">
        {parts.map((p) => (
          <span key={p.label}>
            <i style={{ background: p.color }} />
            {p.label} <strong>{bytes(p.value)}</strong>
          </span>
        ))}
        <span>
          <i className="track" />
          Available <strong>{formatBytes(now.memAvailableBytes)}</strong>
        </span>
      </div>
    </div>
  )
}

function AppsTable({ sys }: { sys: SystemInfo }) {
  const apps = [...sys.apps].sort((a, b) => b.memoryBytes - a.memoryBytes || a.name.localeCompare(b.name))
  return (
    <div className="card section">
      <h3>Apps</h3>
      {apps.length === 0 ? (
        <p className="muted">No projects yet.</p>
      ) : (
        <div className="scroll-x">
          <table className="table">
            <thead>
              <tr>
                <th>App</th>
                <th>Status</th>
                <th className="num">CPU</th>
                <th style={{ width: '28%' }}>Memory of its limit</th>
                <th className="num">Data</th>
                <th className="num">Up</th>
              </tr>
            </thead>
            <tbody>
              {apps.map((a) => {
                const running = a.state === 'running' || a.state === 'crashed'
                const share = a.memoryMaxBytes ? a.memoryBytes / a.memoryMaxBytes : 0
                return (
                  <tr key={a.name} className="clickable" onClick={() => (window.location.hash = `/p/${encodeURIComponent(a.name)}`)}>
                    <td>
                      <a href={`#/p/${encodeURIComponent(a.name)}`}>{a.name}</a>
                    </td>
                    <td>
                      <StatusBadge state={a.state} />
                    </td>
                    <td className="num">{running ? percent(a.cpuPercent) : '—'}</td>
                    <td>
                      {running ? (
                        <div className="meter-cell">
                          <div className={`progress ${share >= 0.9 ? 'high' : ''}`}>
                            <div style={{ width: `${Math.min(share, 1) * 100}%` }} />
                          </div>
                          <span className="small-text muted">
                            {formatBytes(a.memoryBytes)}
                            {a.memoryMaxBytes ? ` of ${formatBytes(a.memoryMaxBytes)}` : ''}
                          </span>
                        </div>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td className="num">{formatBytes(a.dataBytes)}</td>
                    <td className="num">{running ? uptime(a.startedAt) : '—'}</td>
                  </tr>
                )
              })}
              <tr className="table-foot">
                <td>Sideyard</td>
                <td className="muted">{sys.sideyard.version}</td>
                <td className="num">{sys.now.memTotalBytes ? percent(sys.sideyard.cpuPercent) : '—'}</td>
                <td className="muted small-text">{sys.sideyard.memoryBytes ? formatBytes(sys.sideyard.memoryBytes) : '—'}</td>
                <td className="num">—</td>
                <td className="num">{uptime(sys.sideyard.startedAt)}</td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
