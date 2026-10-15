import { lazy, Suspense, useEffect, useRef, useState, type ReactNode } from 'react'
import { Database, Play, ScrollText, Search, SquareTerminal, Table2, X } from 'lucide-react'
import { api, errorMessage, type Project } from './api'
import { useServer } from './App'
import { DeploySetup, hasRememberedToken } from './DeploySetup'
import { appURL, formatBytes, timeAgo, uptime, usePoll } from './lib'
import { PageHead, ReleaseBadge, Stat, StatusBadge } from './ui'
import { VariablesTab } from './VariablesTab'
import { SettingsTab } from './SettingsTab'
import { DatabaseTab } from './DatabaseTab'
import { StorageTab } from './StorageTab'
import { JobsTab } from './JobsTab'
import { EmailTab } from './EmailTab'
import { AreaChart, bytes, NoMetrics, percent, RangePicker, type Range } from './Chart'

// The editors bring the data grid and CodeMirror; load them when opened.
const TableEditor = lazy(() => import('./TableEditor').then((m) => ({ default: m.TableEditor })))
const SqlEditor = lazy(() => import('./SqlEditor').then((m) => ({ default: m.SqlEditor })))
const loading = <div className="editor-empty">Loading…</div>

const sections: Record<string, { title: string; description?: string }> = {
  deployments: {
    title: 'Deployments',
    description: 'Every release uploaded for this project. Roll back to any of the last five.',
  },
  logs: { title: 'Logs', description: "The app's output and Sideyard's events for this project." },
  database: { title: 'Database', description: "The app's SQLite files: download, import, and backups in S3." },
  storage: { title: 'Storage', description: "This project's S3 bucket." },
  jobs: { title: 'Jobs', description: 'Scheduled calls into the app.' },
  email: { title: 'Email', description: 'Who this project sends as, and what it has sent.' },
  variables: { title: 'Variables', description: 'Environment variables and secrets for the app.' },
  settings: { title: 'Settings', description: 'Domains, runtime, deploy token.' },
}

/** Full-bleed sections fill the window; the rest are pages. */
export const flushSections = ['tables', 'sql']

export function ProjectRoutes({ name, section, sub }: { name: string; section: string; sub?: string }) {
  const { data: project, error, reload } = usePoll(() => api.project(name), 4000, name)
  if (section === 'tables') {
    return (
      <Suspense fallback={loading}>
        <TableEditor name={name} table={sub} />
      </Suspense>
    )
  }
  if (section === 'sql') {
    return (
      <Suspense fallback={loading}>
        <SqlEditor name={name} />
      </Suspense>
    )
  }
  if (error && !project)
    return (
      <div className="page">
        <p className="error">{error}</p>
      </div>
    )
  if (!project) return null
  if (section === 'overview' || !sections[section]) return <Overview project={project} onChange={reload} />

  const { title, description } = sections[section]
  return (
    <div className={`page ${section === 'logs' ? '' : 'narrow'}`}>
      <PageHead title={title} description={description} />
      {section === 'deployments' && <DeploymentsTab project={project} onChange={reload} />}
      {section === 'logs' && <LogsTab name={name} />}
      {section === 'database' && <DatabaseTab name={name} onChange={reload} />}
      {section === 'storage' && <StorageTab name={name} />}
      {section === 'jobs' && <JobsTab name={name} />}
      {section === 'email' && <EmailTab name={name} />}
      {section === 'variables' && <VariablesTab name={name} />}
      {section === 'settings' && <SettingsTab project={project} onChange={reload} />}
    </div>
  )
}

function Overview({ project, onChange }: { project: Project; onChange: () => void }) {
  const server = useServer()
  const name = project.name
  const [range, setRange] = useState<Range>('1h')
  const { data: metrics } = usePoll(() => api.metrics(name, range), 15000, `metrics:${name}:${range}`)
  const [busy, setBusy] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const s = project.status
  const live = s.state === 'running' || s.state === 'crashed'
  const rel = project.activeRelease

  async function act(label: string, fn: () => Promise<unknown>) {
    setBusy(label)
    setActionError(null)
    try {
      await fn()
    } catch (e) {
      setActionError(errorMessage(e))
    } finally {
      setBusy(null)
      onChange()
    }
  }

  const cpu = (metrics ?? []).map((m) => ({ t: m.t, v: m.cpu }))
  const mem = (metrics ?? []).map((m) => ({ t: m.t, v: m.mem }))
  const href = (section: string) => `#/p/${encodeURIComponent(name)}/${section}`
  return (
    <div className="page">
      <PageHead
        title={
          <>
            {name} <StatusBadge state={s.state} health={s.health} />
          </>
        }
        description={
          <span className="domains" style={{ marginTop: 0 }}>
            {project.domains.map((d) => (
              <a key={d} href={appURL(d)} target="_blank" rel="noreferrer">
                {d} ↗
              </a>
            ))}
          </span>
        }
      >
        {rel && (
          <button disabled={!!busy} onClick={() => act('restart', () => api.restart(name))}>
            {busy === 'restart' ? 'Restarting…' : live ? 'Restart' : 'Start'}
          </button>
        )}
        {live && (
          <button disabled={!!busy} onClick={() => act('stop', () => api.stop(name))}>
            {busy === 'stop' ? 'Stopping…' : 'Stop'}
          </button>
        )}
      </PageHead>
      {actionError && <div className="banner">{actionError}</div>}
      {s.state === 'running' && s.health.status === 'unhealthy' && (
        <div className="banner">
          Health check failing since {timeAgo(s.health.checkedAt)}: {s.health.error}
        </div>
      )}

      <div className="stats">
        <Stat
          label="Release"
          value={rel ? `#${rel.number}` : '—'}
          note={
            rel ? `${rel.commit ? rel.commit.slice(0, 7) + ' · ' : ''}${timeAgo(rel.createdAt)}` : 'not deployed yet'
          }
        />
        <Stat
          label="Uptime"
          value={s.state === 'running' ? uptime(s.startedAt) : '—'}
          note={
            s.restarts > 0
              ? `${s.restarts} crash restart${s.restarts === 1 ? '' : 's'}`
              : s.state === 'running'
                ? 'no crashes'
                : s.state
          }
        />
        <Stat
          label="Health"
          value={s.health.status === 'healthy' ? 'Healthy' : s.health.status === 'unhealthy' ? 'Failing' : '—'}
          note={s.health.checkedAt ? `checked ${timeAgo(s.health.checkedAt)}` : `GET ${project.healthPath} every 30s`}
        />
        <Stat label="Data" value={formatBytes(project.dataBytes)} note="in DATA_DIR" />
      </div>

      {server && server.resources.memTotalBytes === 0 ? (
        <div style={{ marginBottom: 16 }}>
          <NoMetrics />
        </div>
      ) : (
        <>
          <div className="section-head">
            <h3>Usage</h3>
            <RangePicker value={range} onChange={setRange} />
          </div>
          <div className="charts">
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">CPU</span>
                <strong>{s.state === 'running' ? `${s.cpuPercent.toFixed(s.cpuPercent < 10 ? 1 : 0)}%` : '—'}</strong>
              </div>
              <AreaChart points={cpu} label="CPU, percent of one core" format={percent} floor={1} />
            </div>
            <div className="card chart-card">
              <div className="chart-head">
                <span className="muted">Memory</span>
                <strong>{formatBytes(s.memoryBytes)}</strong>
              </div>
              <AreaChart
                points={mem}
                label="Memory in use"
                format={bytes}
                bytes
                reference={
                  project.memoryMB > 0
                    ? { value: project.memoryMB * 1048576, label: `limit ${project.memoryMB} MB` }
                    : undefined
                }
              />
            </div>
          </div>
        </>
      )}

      <div className="grid2" style={{ marginBottom: 16 }}>
        {[
          { section: 'tables', icon: Table2, title: 'Table Editor', text: 'Browse and edit rows like a spreadsheet.' },
          { section: 'sql', icon: SquareTerminal, title: 'SQL Editor', text: 'Run queries against the live database.' },
          { section: 'database', icon: Database, title: 'Database', text: 'Download, import, and S3 backups.' },
          { section: 'logs', icon: ScrollText, title: 'Logs', text: "The app's output, live." },
        ].map(({ section, icon: Icon, title, text }) => (
          <a key={section} className="card project-card" style={{ minHeight: 0 }} href={href(section)}>
            <div className="field-label">
              <Icon size={16} /> <strong>{title}</strong>
            </div>
            <span className="muted small-text">{text}</span>
          </a>
        ))}
      </div>

      <div className="page-head" style={{ marginBottom: 10 }}>
        <h3>Recent deployments</h3>
        <a className="link-button small-text" href={href('deployments')}>
          View all
        </a>
      </div>
      {(project.releases ?? []).length === 0 ? (
        <div className="card empty">
          <Play size={20} />
          <h3>No deployments yet</h3>
          <p className="muted">
            Push from CI or your laptop: the steps are on the <a href={href('deployments')}>Deployments</a> page.
          </p>
        </div>
      ) : (
        <div className="card list">
          {(project.releases ?? []).slice(0, 5).map((r) => (
            <div key={r.number} className="row release-row">
              <strong className="num">#{r.number}</strong>
              <ReleaseBadge status={r.status} />
              <div className="ellipsis">
                {r.commit && <code>{r.commit.slice(0, 7)}</code>}{' '}
                {r.message || <span className="muted">no message</span>}
              </div>
              <span className="muted num">{formatBytes(r.size)}</span>
              <span className="muted num">{timeAgo(r.createdAt)}</span>
              <span />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}


export function DeploymentsTab({ project, onChange }: { project: Project; onChange: () => void }) {
  const releases = project.releases ?? []
  const [showSetup, setShowSetup] = useState(() => releases.length === 0 || hasRememberedToken(project.name))
  const [open, setOpen] = useState<number | null>(null)
  const [busy, setBusy] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function rollback(n: number) {
    setBusy(n)
    setError(null)
    try {
      await api.rollback(project.name, n)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(null)
      onChange()
    }
  }

  return (
    <>
      {showSetup && (
        <section className="card section">
          <h3>Connect deploys</h3>
          <DeploySetup name={project.name} onDone={() => setShowSetup(releases.length === 0)} />
        </section>
      )}
      {error && <p className="error">{error}</p>}
      {releases.length > 0 && (
        <div className="card list">
          {releases.map((r) => (
            <div key={r.number}>
              <div className="row release-row">
                <strong className="num">#{r.number}</strong>
                <ReleaseBadge status={r.status} />
                <div className="ellipsis">
                  {r.commit && <code>{r.commit.slice(0, 7)}</code>}{' '}
                  {r.message || <span className="muted">no message</span>}
                </div>
                <span className="muted num">{formatBytes(r.size)}</span>
                <span className="muted num">{timeAgo(r.createdAt)}</span>
                <div className="row-actions">
                  {r.status !== 'active' && r.status !== 'failed' && !r.pruned && (
                    <button className="ghost small" disabled={busy !== null} onClick={() => rollback(r.number)}>
                      {busy === r.number ? 'Rolling back…' : 'Roll back'}
                    </button>
                  )}
                  <button className="ghost small" onClick={() => setOpen(open === r.number ? null : r.number)}>
                    {open === r.number ? 'Hide log' : 'Log'}
                  </button>
                </div>
              </div>
              {open === r.number && <pre className="release-log">{r.log || 'No log.'}</pre>}
            </div>
          ))}
        </div>
      )}
      {!showSetup && (
        <button className="link-button" onClick={() => setShowSetup(true)}>
          How do I deploy?
        </button>
      )}
    </>
  )
}

export function LogsTab({ name }: { name: string }) {
  const server = useServer()
  const [live, setLive] = useState(true)
  const [search, setSearch] = useState('')
  const [query, setQuery] = useState('') // search, once typing pauses
  useEffect(() => {
    const t = setTimeout(() => setQuery(search.trim()), 300)
    return () => clearTimeout(t)
  }, [search])
  const { data, error } = usePoll(() => api.logs(name, 1000, query), live ? 2000 : 0, `${name}:${live}:${query}`)
  const ref = useRef<HTMLPreElement>(null)
  const stick = useRef(true)

  useEffect(() => {
    const el = ref.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [data])

  const source = server?.runner === 'systemd' ? 'journald' : 'app.log'
  const found = data?.lines.length ?? 0
  return (
    <div className="card logs">
      <div className="logs-head">
        <div className="search">
          <Search size={14} />
          <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search all logs" />
          {search && (
            <button className="icon" onClick={() => setSearch('')} aria-label="Clear search">
              <X size={13} />
            </button>
          )}
        </div>
        <span className="muted grow small-text">
          {query && data
            ? `${found === 1000 ? 'The newest 1000' : found} matching line${found === 1 ? '' : 's'} in ${source}`
            : `From ${source} · newest at the bottom`}
        </span>
        <label className="toggle">
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} /> Live
        </label>
      </div>
      {error && <p className="error">{error}</p>}
      <pre
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
        }}
      >
        {data?.lines.length
          ? data.lines.map((line, i) => (
              <div key={i} className={line.includes('[sideyard]') ? 'log-sideyard' : undefined}>
                {query ? <Highlight text={line} match={query} /> : line}
              </div>
            ))
          : query
            ? 'No lines match.'
            : 'No output yet.'}
      </pre>
    </div>
  )
}

/** text with each case-insensitive occurrence of match marked. */
function Highlight({ text, match }: { text: string; match: string }) {
  const parts: ReactNode[] = []
  const lower = text.toLowerCase()
  const m = match.toLowerCase()
  let at = 0
  for (let i = lower.indexOf(m); i >= 0 && m; i = lower.indexOf(m, at)) {
    parts.push(text.slice(at, i), <mark key={i}>{text.slice(i, i + m.length)}</mark>)
    at = i + m.length
  }
  parts.push(text.slice(at))
  return <>{parts}</>
}
