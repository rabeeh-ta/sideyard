import { useEffect, useState, type FormEvent } from 'react'
import { api, errorMessage, type BackupSettings } from './api'
import { duration, formatBytes, formatDate, timeAgo, usePoll } from './lib'
import { PageHead, Pill, Section } from './ui'
import { BucketFields } from './BucketFields'

type Status = { ok: boolean; message: string } | null

/** Off-server copies of every database: the bucket, the schedule, and the runs so far. */
export function BackupsPage() {
  const { data, error, reload } = usePoll(api.settings, 0, 'settings')
  return (
    <div className="page narrow">
      <PageHead
        title="Backups"
        description="Copies every app's databases, and Sideyard's own, to an S3-compatible bucket on a schedule."
      />
      {error && <p className="error">{error}</p>}
      {data && <BackupsForm initial={data.backups} onSaved={reload} />}
    </div>
  )
}

function BackupsForm({ initial, onSaved }: { initial: BackupSettings; onSaved: () => void }) {
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
    <>
      <Section title="Bucket and schedule" description="Use a bucket only for backups.">
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
            The newest three copies are always kept. The master key isn't uploaded: download it in{' '}
            <a href="#/settings/security">Platform settings → Security</a>.
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
      </Section>

      {runs && runs.length > 0 && (
        <Section title="Recent backups">
          <div className="kv">
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
        </Section>
      )}
    </>
  )
}
