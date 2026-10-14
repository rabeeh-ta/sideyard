import { useState, type FormEvent } from 'react'
import { api, errorMessage, type DomainStatus, type Project } from './api'
import { useServer } from './App'
import { DeploySetup } from './DeploySetup'
import { go, usePoll } from './lib'
import { Pill, Section } from './ui'

export function SettingsTab({ project, onChange }: { project: Project; onChange: () => void }) {
  return (
    <>
      <DomainsSection project={project} onChange={onChange} />
      <RuntimeSection project={project} onChange={onChange} />
      <Section title="Deploy token" description="CI uses this token to upload releases.">
        <DeploySetup name={project.name} />
      </Section>
      <DeleteSection name={project.name} />
    </>
  )
}

function DomainsSection({ project, onChange }: { project: Project; onChange: () => void }) {
  const server = useServer()
  const [host, setHost] = useState('')
  const [error, setError] = useState<string | null>(null)
  const { data: statuses, reload } = usePoll(() => api.domainStatus(project.name), 0, project.domains.join(','))

  async function run(fn: () => Promise<unknown>) {
    setError(null)
    try {
      await fn()
      setHost('')
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      onChange()
      reload()
    }
  }

  return (
    <Section
      title="Domains"
      description={
        server?.dev
          ? 'Dev mode: *.localhost names resolve to this machine in most browsers.'
          : 'Point an A record for each domain at this server. HTTPS certificates are issued automatically.'
      }
    >
      <div className="kv">
        {project.domains.map((d) => (
          <div key={d} className="kv-row domain">
            <div>
              <code className="kv-key">{d}</code>
              <DomainChecks status={statuses?.find((x) => x.host === d)} dev={!!server?.dev} />
            </div>
            <div className="row-actions">
              <button className="ghost small" onClick={() => run(() => api.removeDomain(project.name, d))}>
                Remove
              </button>
            </div>
          </div>
        ))}
      </div>
      <form
        className="inline-form"
        onSubmit={(e) => {
          e.preventDefault()
          run(() => api.addDomain(project.name, host))
        }}
      >
        <input className="grow" placeholder="app.example.com" value={host} onChange={(e) => setHost(e.target.value)} />
        <button className="ghost" disabled={!host.trim()}>
          Add domain
        </button>
      </form>
      {error && <p className="error">{error}</p>}
    </Section>
  )
}

function RuntimeSection({ project, onChange }: { project: Project; onChange: () => void }) {
  const server = useServer()
  const [args, setArgs] = useState(project.args)
  const [healthPath, setHealthPath] = useState(project.healthPath)
  const [memoryMB, setMemoryMB] = useState(String(project.memoryMB))
  const [cpuPercent, setCPUPercent] = useState(String(project.cpuPercent))
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const dirty =
    args !== project.args ||
    healthPath !== project.healthPath ||
    Number(memoryMB) !== project.memoryMB ||
    Number(cpuPercent) !== project.cpuPercent

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setStatus(null)
    try {
      await api.updateSettings(project.name, {
        healthPath,
        args,
        memoryMB: Number(memoryMB) || 0,
        cpuPercent: Number(cpuPercent) || 0,
      })
      setStatus({ ok: true, message: 'Saved.' })
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    } finally {
      setBusy(false)
      onChange()
    }
  }

  return (
    <Section title="Runtime">
      <form onSubmit={submit} className="stack">
        <label className="field">
          <span>Start arguments</span>
          <input
            placeholder="none"
            value={args}
            onChange={(e) => setArgs(e.target.value)}
            spellCheck={false}
          />
          <small className="muted">
            Passed to your binary; <code>$PORT</code> and <code>$DATA_DIR</code> are expanded. For PocketBase:{' '}
            <code>serve --http=127.0.0.1:$PORT --dir=$DATA_DIR/pb_data</code>. Changing them restarts the app.
          </small>
        </label>
        <label className="field">
          <span>Health check path</span>
          <input value={healthPath} onChange={(e) => setHealthPath(e.target.value)} spellCheck={false} />
          <small className="muted">
            A deploy goes live only after this path answers with a 2xx or 3xx. PocketBase uses{' '}
            <code>/api/health</code>.
          </small>
        </label>
        <div className="limits">
          <label className="field">
            <span>Memory limit (MB)</span>
            <input type="number" min={0} value={memoryMB} onChange={(e) => setMemoryMB(e.target.value)} />
          </label>
          <label className="field">
            <span>CPU limit (%)</span>
            <input type="number" min={0} value={cpuPercent} onChange={(e) => setCPUPercent(e.target.value)} />
          </label>
        </div>
        <small className="muted limits-help">
          0 means no limit; 100% is one full core. An app that goes over its memory limit is restarted.{' '}
          {server?.runner === 'process'
            ? 'This server runs apps as plain processes, so limits are saved but not enforced (they are on Linux with systemd).'
            : 'Changing a limit restarts the app with no downtime.'}
        </small>
        <div>
          <button className="primary" disabled={busy || !dirty}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
        {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
      </form>
    </Section>
  )
}

function DeleteSection({ name }: { name: string }) {
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)

  async function remove() {
    try {
      await api.deleteProject(name, confirm)
      go('/')
    } catch (e) {
      setError(errorMessage(e))
    }
  }

  return (
    <Section
      title="Delete project"
      description="Stops the app and permanently deletes its releases, database, files in DATA_DIR and logs."
    >
      <div className="inline-form">
        <input className="grow" placeholder={`Type ${name} to confirm`} value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        <button className="danger" disabled={confirm !== name} onClick={remove}>
          Delete
        </button>
      </div>
      {error && <p className="error">{error}</p>}
    </Section>
  )
}

/** Whether a domain resolves to this server and has a certificate. */
function DomainChecks({ status, dev }: { status?: DomainStatus; dev: boolean }) {
  if (!status) return null
  const daysLeft = status.certificate
    ? Math.floor((new Date(status.certificate.notAfter).getTime() - Date.now()) / 86400000)
    : 0
  return (
    <div className="domain-status" style={{ marginTop: 6 }}>
      {status.dnsError ? (
        <Pill tone="red">DNS: {status.dnsError}</Pill>
      ) : status.pointsHere === false ? (
        <Pill tone="red">DNS points elsewhere: {status.addresses.join(', ')}</Pill>
      ) : status.pointsHere ? (
        <Pill tone="green">DNS points here</Pill>
      ) : (
        <Pill tone="grey">DNS: {status.addresses.join(', ')}</Pill>
      )}
      {dev ? (
        <Pill tone="grey">dev mode: plain HTTP</Pill>
      ) : status.certificate ? (
        <Pill tone={daysLeft < 14 ? 'amber' : 'green'}>
          HTTPS by {status.certificate.issuer}, renews automatically ({daysLeft} days left)
        </Pill>
      ) : (
        <Pill tone="amber">no certificate yet: issued on the first visit once DNS points here</Pill>
      )}
    </div>
  )
}
