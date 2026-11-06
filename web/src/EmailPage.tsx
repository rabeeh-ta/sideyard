import { useEffect, useState, type FormEvent } from 'react'
import { api, errorMessage, type EmailSettings } from './api'
import { usePoll } from './lib'
import { PageHead, Section } from './ui'

type Status = { ok: boolean; message: string } | null

/** The one SMTP provider every app sends through. */
export function EmailPage() {
  const { data, error, reload } = usePoll(api.settings, 0, 'settings')
  return (
    <div className="page narrow">
      <PageHead
        title="Email"
        description="One SMTP provider for every app (Resend, Postmark, SES, Mailgun…). Apps send through Sideyard and never see these credentials."
      />
      {error && <p className="error">{error}</p>}
      {data && <EmailForm initial={data.email} onSaved={reload} />}
    </div>
  )
}

function EmailForm({ initial, onSaved }: { initial: EmailSettings; onSaved: () => void }) {
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
    <Section title="SMTP provider">
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
