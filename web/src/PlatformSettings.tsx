import { useEffect, useState, type FormEvent } from 'react'
import { api, errorMessage } from './api'
import { saveBlob, usePoll } from './lib'
import { Section } from './ui'
import { UpdatesSection } from './Updates'
import { useServer } from './App'

type Status = { ok: boolean; message: string } | null

const sections = [
  ['updates', 'Updates'],
  ['security', 'Security'],
  ['server', 'Server'],
] as const

export function PlatformSettings({ focus }: { focus?: string }) {
  const { data, error } = usePoll(api.settings, 0, 'settings')
  const server = useServer()
  useEffect(() => {
    if (focus && focus !== 'updates' && data) document.getElementById(focus)?.scrollIntoView({ block: 'start' })
  }, [focus, data !== null])
  if (error) return <p className="error">{error}</p>
  if (!data) return null
  return (
    <>
      <div className="page-head" style={{ marginBottom: 12 }}>
        <div>
          <h1>Platform settings</h1>
          <p>Sideyard's updates, security and server details.</p>
        </div>
      </div>
      <nav className="subnav">
        {sections.map(([id, label]) => (
          <a key={id} href={`#/settings/${id}`} className={focus === id ? 'active' : ''}>
            {label}
          </a>
        ))}
      </nav>
      <UpdatesSection focus={focus === 'updates'} />
      <SecuritySection />
      <Section title="Server" id="server">
        <div className="kv">
          {[
            ['Domain', data.server.domain],
            ['Dashboard', data.server.dashboardHost],
            ['Data directory', data.server.dataDir],
            ['App runner', data.server.runner === 'systemd' ? 'systemd (isolated units per app)' : 'plain processes (dev mode)'],
            ['Mode', data.server.dev ? 'development (HTTP)' : 'production (HTTPS)'],
            ['Version', server?.version ?? ''],
          ].map(([k, v]) => (
            <div key={k} className="kv-row builtin">
              <span className="muted">{k}</span>
              <code className="kv-key">{v}</code>
            </div>
          ))}
        </div>
      </Section>
    </>
  )
}

function SecuritySection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [keyPassword, setKeyPassword] = useState('')
  const [status, setStatus] = useState<Status>(null)

  async function change(e: FormEvent) {
    e.preventDefault()
    setStatus(null)
    try {
      await api.changePassword(current, next)
      setStatus({ ok: true, message: 'Password changed. Other browsers have been signed out.' })
      setCurrent('')
      setNext('')
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    }
  }

  async function downloadKey(e: FormEvent) {
    e.preventDefault()
    setStatus(null)
    try {
      saveBlob(await api.masterKey(keyPassword), 'sideyard-master.key')
      setKeyPassword('')
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    }
  }

  return (
    <Section title="Security" id="security">
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
      <form className="inline-form" onSubmit={change}>
        <label className="field grow">
          <span>Current password</span>
          <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />
        </label>
        <label className="field grow">
          <span>New password (12+ characters)</span>
          <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
        </label>
        <button className="primary" disabled={!current || next.length < 12}>
          Change password
        </button>
      </form>
      <form className="field" onSubmit={downloadKey} style={{ marginTop: 18 }}>
        <span>Master key</span>
        <div className="inline-form">
          <input
            className="grow"
            type="password"
            value={keyPassword}
            onChange={(e) => setKeyPassword(e.target.value)}
            placeholder="your password, to confirm"
            aria-label="Your password, to download the master key"
            autoComplete="current-password"
          />
          <button className="ghost" disabled={!keyPassword}>
            Download master key
          </button>
        </div>
        <small className="muted">Decrypts every saved secret. Keep a copy off the server: restoring from backups needs it.</small>
      </form>
    </Section>
  )
}
