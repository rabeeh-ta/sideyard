import { useState, type FormEvent } from 'react'
import { Plus, Search } from 'lucide-react'
import { api, errorMessage } from './api'
import { useServer } from './App'
import { formatBytes, go, timeAgo, usePoll } from './lib'
import { percent } from './Chart'
import { Drawer, PageHead, StatusBadge } from './ui'
import { rememberToken } from './DeploySetup'

export function ProjectsPage() {
  const { data: projects, error } = usePoll(api.projects, 5000, 'projects')
  const server = useServer()
  const [creating, setCreating] = useState(false)
  const [search, setSearch] = useState('')
  const shown = (projects ?? []).filter((p) => p.name.includes(search.toLowerCase()))

  return (
    <div className="page">
      <PageHead title="Projects" description={server ? `Apps on ${server.domain}` : undefined}>
        <button className="primary" onClick={() => setCreating(true)}>
          <Plus size={14} /> New project
        </button>
      </PageHead>

      <div className="toolbar">
        <div className="search">
          <Search size={14} />
          <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search projects" />
        </div>
      </div>

      {error && <p className="error">{error}</p>}
      {projects && projects.length === 0 && (
        <div className="card empty">
          <h3>No projects yet</h3>
          <p className="muted">
            Create a project, then push your app. Each one gets a subdomain, a SQLite database and logs.
          </p>
          <button className="primary" onClick={() => setCreating(true)}>
            <Plus size={14} /> New project
          </button>
        </div>
      )}
      <div className="project-grid">
        {shown.map((p) => (
          <a key={p.name} className="card project-card" href={`#/p/${encodeURIComponent(p.name)}`}>
            <div className="project-card-head">
              <div style={{ minWidth: 0 }}>
                <h3>{p.name}</h3>
                <div className="domain ellipsis">{p.domains[0] ?? 'no domain'}</div>
              </div>
              <StatusBadge state={p.status.state} health={p.status.health} />
            </div>
            <div className="project-card-foot">
              <span>
                {p.activeRelease
                  ? `#${p.activeRelease.number} · ${timeAgo(p.activeRelease.createdAt)}`
                  : 'not deployed'}
              </span>
              {p.status.state === 'running' && <span>{formatBytes(p.status.memoryBytes) || '—'} RAM</span>}
              {p.status.state === 'running' && <span>{percent(p.status.cpuPercent)} CPU</span>}
            </div>
          </a>
        ))}
      </div>

      {creating && <NewProject onClose={() => setCreating(false)} />}
    </div>
  )
}

function NewProject({ onClose }: { onClose: () => void }) {
  const server = useServer()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const slug = name.trim().toLowerCase()

  async function submit(e?: FormEvent) {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const { project, deployToken } = await api.createProject(name)
      rememberToken(project.name, deployToken)
      go(`/p/${encodeURIComponent(project.name)}/deployments`)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title="Create a new project"
      onClose={onClose}
      footer={
        <>
          {error && <span className="error grow small-text">{error}</span>}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy || !slug} onClick={() => submit()}>
            {busy ? 'Creating…' : 'Create project'}
          </button>
        </>
      }
    >
      <form onSubmit={submit} className="stack" style={{ marginTop: 0 }}>
        <label className="field">
          <span>Name</span>
          <input
            autoFocus
            placeholder="my-app"
            value={name}
            onChange={(e) => setName(e.target.value)}
            spellCheck={false}
          />
          <small className="muted">Lowercase letters, digits and dashes.</small>
        </label>
        {slug && server && (
          <p className="muted">
            It will be served at{' '}
            <code>
              {slug}.{server.domain}
            </code>
            , with its own SQLite database and logs. Next you'll see how to deploy to it.
          </p>
        )}
      </form>
    </Drawer>
  )
}
