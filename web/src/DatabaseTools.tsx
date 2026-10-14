import { useState } from 'react'
import { api, errorMessage } from './api'
import { formatBytes, formatDate, usePoll } from './lib'
import { ConfirmButton, Section } from './ui'

/** Copies of this database in the backup bucket, with restore. */
export function RemoteBackups({ name, path, onRestored }: { name: string; path: string; onRestored: () => void }) {
  const { data, error, reload } = usePoll(() => api.remoteBackups(name, path), 0, `${name}:${path}`)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [busy, setBusy] = useState(false)

  async function restore(key: string) {
    setBusy(true)
    setStatus(null)
    try {
      const res = await api.restoreBackup(name, path, key)
      setStatus({
        ok: true,
        message: `Restored ${path}.${res.backup ? ` The version it replaced is kept as ${res.backup}.` : ''}`,
      })
      onRestored()
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    } finally {
      setBusy(false)
      reload()
    }
  }

  return (
    <Section
      title="Backups in S3"
      description={
        data && !data.enabled ? (
          <>
            Off. Connect a backup bucket in <a href="#/settings">Platform settings</a> to copy every database off the server
            on a schedule.
          </>
        ) : (
          'Scheduled copies of this database. Restoring works like an import: the app restarts with the copy, and the current file is kept.'
        )
      }
    >
      {error && <p className="error">{error}</p>}
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
      {data?.enabled && data.backups.length === 0 && <p className="muted">No copies yet.</p>}
      <div className="kv">
        {data?.backups.slice(0, 30).map((b) => (
          <div key={b.key} className="kv-row">
            <span>{formatDate(b.takenAt)}</span>
            <span className="muted">{formatBytes(b.size)} gzipped</span>
            <span />
            <div className="row-actions">
              <a className="button-link" href={api.remoteBackupURL(name, path, b.key)}>
                Download
              </a>
              <ConfirmButton label="Restore" confirmLabel="Restore this copy" disabled={busy} onConfirm={() => restore(b.key)} />
            </div>
          </div>
        ))}
      </div>
    </Section>
  )
}
