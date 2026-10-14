import { createContext, useContext, useEffect, useState, type FormEvent } from 'react'
import {
  Archive,
  ArrowUpCircle,
  Check,
  ChevronsUpDown,
  Clock,
  Database,
  House,
  KeyRound,
  LayoutGrid,
  LogOut,
  Mail,
  Moon,
  Rocket,
  ScrollText,
  Server,
  Settings,
  SlidersHorizontal,
  SquareTerminal,
  Sun,
  Table2,
  type LucideIcon,
} from 'lucide-react'
import { api, errorMessage, type ServerInfo } from './api'
import { formatBytes, go, usePoll, useRoute, useTheme } from './lib'
import { ProjectsPage } from './ProjectsPage'
import { flushSections, ProjectRoutes } from './ProjectPage'
import { PlatformSettings } from './PlatformSettings'
import { SystemPage } from './SystemPage'
import { Logo, Menu } from './ui'

const ServerContext = createContext<ServerInfo | null>(null)
export const useServer = () => useContext(ServerContext)

export function App() {
  const [auth, setAuth] = useState<'loading' | 'in' | 'out'>('loading')

  useEffect(() => {
    api.session().then(
      (s) => setAuth(s.authenticated ? 'in' : 'out'),
      () => setAuth('out'),
    )
    const onSignedOut = () => setAuth('out')
    window.addEventListener('sideyard:signed-out', onSignedOut)
    return () => window.removeEventListener('sideyard:signed-out', onSignedOut)
  }, [])

  if (auth === 'loading') return null
  if (auth === 'out') return <Login onSignedIn={() => setAuth('in')} />
  return <Shell onSignedOut={() => setAuth('out')} />
}

function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api.login(password)
      onSignedIn()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <span className="logo">
          <Logo size={20} /> sideyard
        </span>
        <div>
          <h3>Welcome back</h3>
          <p className="muted" style={{ margin: 0 }}>
            Sign in to your Sideyard.
          </p>
        </div>
        <label className="field">
          <span>Admin password</span>
          <input type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <p className="error">{error}</p>}
        <button className="primary" disabled={busy || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
        <p className="hint">
          Forgot it? Run <code>sudo sideyard reset-password</code> on the server.
        </p>
      </form>
    </div>
  )
}

type NavItem = { section: string; label: string; icon: LucideIcon } | 'sep'

const projectNav: NavItem[] = [
  { section: 'overview', label: 'Overview', icon: House },
  { section: 'tables', label: 'Table Editor', icon: Table2 },
  { section: 'sql', label: 'SQL Editor', icon: SquareTerminal },
  { section: 'database', label: 'Database', icon: Database },
  { section: 'storage', label: 'Storage', icon: Archive },
  { section: 'jobs', label: 'Jobs', icon: Clock },
  { section: 'email', label: 'Email', icon: Mail },
  'sep',
  { section: 'deployments', label: 'Deployments', icon: Rocket },
  { section: 'logs', label: 'Logs', icon: ScrollText },
  { section: 'variables', label: 'Variables', icon: KeyRound },
  { section: 'settings', label: 'Project settings', icon: Settings },
]

function Shell({ onSignedOut }: { onSignedOut: () => void }) {
  const route = useRoute()
  const [theme, toggleTheme] = useTheme()
  const { data: server } = usePoll(api.server, 5000, 'server')
  const { data: update } = usePoll(api.update, 10 * 60 * 1000, 'update')
  const project = route[0] === 'p' ? route[1] : null
  const section = project ? route[2] || 'overview' : route[0] === 'settings' || route[0] === 'system' ? route[0] : 'projects'

  async function signOut() {
    await api.logout().catch(() => {})
    onSignedOut()
  }

  const r = server?.resources
  return (
    <ServerContext.Provider value={server}>
      <div className="app">
        <header className="header">
          <div className="header-left">
            <a href="#/" className="home-mark" title="All projects">
              <Logo />
            </a>
            <a href="#/" className="crumb">
              sideyard
            </a>
            {project && (
              <>
                <span className="crumb-sep">/</span>
                <ProjectSwitcher current={project} section={section} />
              </>
            )}
            {!project && section !== 'projects' && (
              <>
                <span className="crumb-sep">/</span>
                <span className="crumb">{section === 'system' ? 'System' : 'Platform settings'}</span>
              </>
            )}
          </div>
          <div className="header-right">
            {update?.available && update.state === 'idle' && (
              <a className="update-pill" href="#/settings/updates" title={`Sideyard ${update.latest} is available`}>
                <ArrowUpCircle size={13} /> Update to {update.latest}
              </a>
            )}
            {r && (
              <a className="usage" href="#/system" title="System">
                <span>
                  CPU <strong>{Math.round(r.cpuPercent)}%</strong>
                </span>
                {r.memTotalBytes > 0 && (
                  <span>
                    RAM <strong>{formatBytes(r.memTotalBytes - r.memAvailableBytes)}</strong> / {formatBytes(r.memTotalBytes)}
                  </span>
                )}
                <span>
                  Disk <strong>{formatBytes(r.diskUsedBytes)}</strong> / {formatBytes(r.diskTotalBytes)}
                </span>
              </a>
            )}
            <button className="icon" onClick={toggleTheme} title={theme === 'dark' ? 'Light theme' : 'Dark theme'}>
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <button className="icon" onClick={signOut} title="Sign out">
              <LogOut size={16} />
            </button>
          </div>
        </header>

        <div className="body">
          <nav className="sidebar">
            {project ? (
              projectNav.map((item, i) =>
                item === 'sep' ? (
                  <div key={i} className="side-sep" />
                ) : (
                  <a
                    key={item.section}
                    href={`#/p/${encodeURIComponent(project)}/${item.section === 'overview' ? '' : item.section}`}
                    className={`side-item ${section === item.section ? 'active' : ''}`}
                  >
                    <item.icon size={18} />
                    <span>{item.label}</span>
                  </a>
                ),
              )
            ) : (
              <>
                <a href="#/" className={`side-item ${section === 'projects' ? 'active' : ''}`}>
                  <LayoutGrid size={18} />
                  <span>Projects</span>
                </a>
                <a href="#/system" className={`side-item ${section === 'system' ? 'active' : ''}`}>
                  <Server size={18} />
                  <span>System</span>
                </a>
              </>
            )}
            <div className="side-spacer" />
            <div className="side-sep" />
            {project && (
              <>
                <a href="#/" className="side-item">
                  <LayoutGrid size={18} />
                  <span>All projects</span>
                </a>
                <a href="#/system" className="side-item">
                  <Server size={18} />
                  <span>System</span>
                </a>
              </>
            )}
            <a href="#/settings" className={`side-item ${route[0] === 'settings' ? 'active' : ''}`}>
              <SlidersHorizontal size={18} />
              <span>Platform settings</span>
            </a>
          </nav>
          <main className={`main ${project && flushSections.includes(section) ? 'flush' : ''}`}>
            {project ? (
              <ProjectRoutes name={project} section={section} sub={route[3]} />
            ) : route[0] === 'settings' ? (
              <div className="page narrow">
                <PlatformSettings focus={route[1]} />
              </div>
            ) : route[0] === 'system' ? (
              <SystemPage />
            ) : (
              <ProjectsPage />
            )}
          </main>
        </div>
      </div>
    </ServerContext.Provider>
  )
}

/** The project name in the header, opening a menu to switch projects. */
function ProjectSwitcher({ current, section }: { current: string; section: string }) {
  const { data: projects } = usePoll(api.projects, 0, 'switcher')
  return (
    <Menu
      trigger={() => (
        <button>
          {current} <ChevronsUpDown size={14} className="faint" />
        </button>
      )}
    >
      {(close) => (
        <>
          <div className="menu-label">Projects</div>
          {projects?.map((p) => (
            <button
              key={p.name}
              className="menu-item"
              onClick={() => {
                close()
                go(`/p/${encodeURIComponent(p.name)}/${section === 'overview' ? '' : section}`)
              }}
            >
              <span className={`badge state-${p.status.state}`} style={{ padding: 0, border: 'none', background: 'none' }}>
                <span className="dot" />
              </span>
              {p.name}
              {p.name === current && <Check size={14} className="check" />}
            </button>
          ))}
          <div className="menu-sep" />
          <a className="menu-item" href="#/" onClick={close}>
            <LayoutGrid size={14} /> All projects
          </a>
        </>
      )}
    </Menu>
  )
}
