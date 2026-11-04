import { useState, type FormEvent } from 'react'
import { api, errorMessage, type Project } from './api'
import { useServer } from './App'
import { DeploySetup } from './DeploySetup'
import { go } from './lib'
import { Section } from './ui'

export function SettingsTab({ project, onChange }: { project: Project; onChange: () => void }) {
  return (
    <>
      <RuntimeSection project={project} onChange={onChange} />
      <Section title="Deploy token" description="CI uses this token to upload releases.">
        <DeploySetup name={project.name} steps={false} />
      </Section>
      <DeleteSection name={project.name} />
    </>
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
            Passed to your binary, with <code>$PORT</code> and <code>$DATA_DIR</code> expanded.
          </small>
        </label>
        <label className="field">
          <span>Health check path</span>
          <input value={healthPath} onChange={(e) => setHealthPath(e.target.value)} spellCheck={false} />
          <small className="muted">A deploy goes live once this path answers with a 2xx or 3xx.</small>
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
          0 means no limit; 100% is one core. Saving restarts the app without downtime.
          {server?.runner === 'process' && ' Limits are enforced only by the systemd runner, not on this server.'}
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
