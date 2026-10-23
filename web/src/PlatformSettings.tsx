import { useEffect, useState, type FormEvent } from 'react'
import { api, errorMessage, type BackupSettings, type EmailSettings } from './api'
import { duration, formatBytes, formatDate, saveBlob, timeAgo, usePoll } from './lib'
import { Pill, Section } from './ui'
import { BucketFields } from './BucketFields'
import { UpdatesSection } from './Updates'
import { useServer } from './App'

type Status = { ok: boolean; message: string } | null

const sections = [
  ['updates', 'Updates'],
  ['backups', 'Backups'],
  ['email', 'Email'],
  ['security', 'Security'],
  ['server', 'Server'],
] as const

export function PlatformSettings({ focus }: { focus?: string }) {
  const { data, error, reload } = usePoll(api.settings, 0, 'settings')
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
          <p>Settings for every project on this server.</p>
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
      <BackupsSection initial={data.backups} onSaved={reload} />
      <EmailSection initial={data.email} onSaved={reload} />
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

function BackupsSection({ initial, onSaved }: { initial: BackupSettings; onSaved: () => void }) {
  const [form, setForm] = useState<BackupSettings>({ ...initial, secretKey: '' })
  const [status, setStatus] = useState<Status>(null)
  const [busy, setBusy] = useState(false)
  const { data: runs, reload: reloadRuns } = usePoll(api.backupRuns, 5000, 'backup-runs')
  const [open, setOpen] = useState<number | null>(null)
  const set = <K extends keyof BackupSettings>(k: K, v: BackupSettings[K]) => setForm({ ...form, [k]: v })

  useEffect(() => setForm({ ...initial, secretKey: '' }), [initial])

  async function save(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setStatus(null)
    try {
      await api.saveBackups(form)
      setStatus({ ok: true, message: form.enabled ? 'Saved. Sideyard could write to the bucket.' : 'Saved. Backups are off.' })
      onSaved()
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    } finally {
      setBusy(false)
    }
  }

  async function runNow() {
    setStatus(null)
    try {
      await api.runBackup()
      reloadRuns()
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    }
  }

  const running = runs?.some((r) => r.status === 'running')
  return (
    <Section
      id="backups"
      title="Backups"
      description="Copies every app's databases, and Sideyard's own, to an S3-compatible bucket on a schedule. Use a bucket only for backups."
    >
      <form className="stack" onSubmit={save}>
        <label className="toggle">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} /> Back up
          automatically
        </label>
        <BucketFields
          // A saved change reloads the settings; start again from them.
          key={`${initial.endpoint}|${initial.region}`}
          value={form}
          onChange={(v) => setForm({ ...form, ...v })}
          secretSaved={initial.hasSecret}
        />
        <div className="grid3">
          <label className="field">
            <span>Folder in the bucket</span>
            <input value={form.prefix} onChange={(e) => set('prefix', e.target.value)} placeholder="sideyard" spellCheck={false} />
          </label>
          <label className="field">
            <span>Every (hours)</span>
            <input
              type="number"
              min={1}
              max={168}
              value={form.intervalHours}
              onChange={(e) => set('intervalHours', Number(e.target.value))}
            />
          </label>
          <label className="field">
            <span>Keep for (days)</span>
            <input
              type="number"
              min={1}
              value={form.retentionDays}
              onChange={(e) => set('retentionDays', Number(e.target.value))}
            />
          </label>
        </div>
        <small className="muted">
          The newest three copies are always kept. The master key isn't uploaded: download it under Security.
        </small>
        {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
        <div className="inline-form">
          <button className="primary" disabled={busy}>
            {busy ? 'Checking the bucket…' : 'Save'}
          </button>
          <button type="button" className="ghost" disabled={!initial.enabled || running} onClick={runNow}>
            {running ? 'Backing up…' : 'Back up now'}
          </button>
        </div>
      </form>

      {runs && runs.length > 0 && (
        <div className="kv" style={{ marginTop: 18 }}>
          {runs.map((r) => (
            <div key={r.id}>
              <div className="kv-row">
                <span>
                  <Pill
                    tone={
                      r.status === 'succeeded' ? 'green' : r.status === 'running' ? 'amber' : r.status === 'partial' ? 'amber' : 'red'
                    }
                  >
                    {r.status}
                  </Pill>
                </span>
                <span className="muted small-text">
                  {formatDate(r.startedAt)} · {r.trigger} · {duration(r.startedAt, r.finishedAt)} · {r.files} files,{' '}
                  {formatBytes(r.bytes)}
                </span>
                <span className="muted small-text">{timeAgo(r.startedAt)}</span>
                <div className="row-actions">
                  {r.log && (
                    <button className="ghost small" onClick={() => setOpen(open === r.id ? null : r.id)}>
                      {open === r.id ? 'Hide log' : 'Log'}
                    </button>
                  )}
                </div>
              </div>
              {open === r.id && <pre className="run-output">{r.log}</pre>}
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

function EmailSection({ initial, onSaved }: { initial: EmailSettings; onSaved: () => void }) {
  const [form, setForm] = useState<EmailSettings>({ ...initial, password: '' })
  const [domains, setDomains] = useState(initial.senderDomains.join(', '))
  const [to, setTo] = useState('')
  const [status, setStatus] = useState<Status>(null)
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof EmailSettings>(k: K, v: EmailSettings[K]) => setForm({ ...form, [k]: v })

  useEffect(() => {
    setForm({ ...initial, password: '' })
    setDomains(initial.senderDomains.join(', '))
  }, [initial])

  async function save(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setStatus(null)
    try {
      await api.saveEmail({ ...form, senderDomains: domains.split(',').map((d) => d.trim()).filter(Boolean) })
      setStatus({ ok: true, message: form.enabled ? 'Saved. Sideyard logged in to the provider.' : 'Saved. Email is off.' })
      onSaved()
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    } finally {
      setBusy(false)
    }
  }

  async function test() {
    setStatus(null)
    try {
      await api.testEmail(to)
      setStatus({ ok: true, message: `Test email sent to ${to}.` })
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    }
  }

  return (
    <Section
      id="email"
      title="Email"
      description="One SMTP provider for every app (Resend, Postmark, SES, Mailgun…). Apps send through Sideyard and never see these credentials."
    >
      <form className="stack" onSubmit={save}>
        <label className="toggle">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} /> Let apps send
          email
        </label>
        <div className="grid3">
          <label className="field">
            <span>SMTP host</span>
            <input value={form.host} onChange={(e) => set('host', e.target.value)} placeholder="smtp.resend.com" spellCheck={false} />
          </label>
          <label className="field">
            <span>Port</span>
            <input type="number" value={form.port} onChange={(e) => set('port', Number(e.target.value))} />
          </label>
          <label className="field">
            <span>Security</span>
            <select value={form.security} onChange={(e) => set('security', e.target.value as EmailSettings['security'])}>
              <option value="starttls">STARTTLS (587)</option>
              <option value="tls">TLS (465)</option>
              <option value="none">None (local relay only)</option>
            </select>
          </label>
        </div>
        <div className="grid2">
          <label className="field">
            <span>Username</span>
            <input value={form.username} onChange={(e) => set('username', e.target.value)} placeholder="resend" autoComplete="off" />
          </label>
          <label className="field">
            <span>Password or API key</span>
            <input
              type="password"
              value={form.password}
              onChange={(e) => set('password', e.target.value)}
              placeholder={initial.hasPassword ? 'saved; leave empty to keep it' : ''}
              autoComplete="new-password"
            />
          </label>
          <label className="field">
            <span>Default sender</span>
            <input value={form.from} onChange={(e) => set('from', e.target.value)} placeholder="Sideyard <hello@example.com>" />
          </label>
          <label className="field">
            <span>Sender domains</span>
            <input value={domains} onChange={(e) => setDomains(e.target.value)} placeholder="example.com, notes.app" />
            <small className="muted">The domains verified with the provider. Projects can only send from these.</small>
          </label>
        </div>
        {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
        <div className="inline-form">
          <button className="primary" disabled={busy}>
            {busy ? 'Logging in to the provider…' : 'Save'}
          </button>
          <span className="grow" />
          <input type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" style={{ maxWidth: 240 }} />
          <button type="button" className="ghost" disabled={!initial.enabled || !to} onClick={test}>
            Send test
          </button>
        </div>
      </form>
    </Section>
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
