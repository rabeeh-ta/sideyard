import { useRef, useState } from 'react'
import { api, errorMessage, type Database } from './api'
import { formatBytes, timeAgo, usePoll } from './lib'
import { Section } from './ui'
import { RemoteBackups } from './DatabaseTools'

export function DatabaseTab({ name, onChange }: { name: string; onChange: () => void }) {
  const { data, error, reload } = usePoll(() => api.databases(name), 0, name)
  const [path, setPath] = useState<string | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const importRef = useRef<HTMLDivElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const target = path ?? data?.databases[0]?.path ?? 'app.db'
  const [chosen, setSelected] = useState<string | null>(null)
  const selected = chosen ?? data?.databases[0]?.path ?? ''
  const busy = progress !== null

  async function runImport() {
    if (!file) return
    setStatus(null)
    setProgress(0)
    try {
      const res = await api.importDatabase(name, target, file, setProgress)
      setStatus({
        ok: true,
        message: res.backup
          ? `Imported ${res.path}. The previous version is kept as ${res.backup}.`
          : `Imported ${res.path}.`,
      })
      setFile(null)
      if (fileRef.current) fileRef.current.value = ''
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    } finally {
      setProgress(null)
      reload()
      onChange()
    }
  }

  function download(db: Database) {
    window.location.href = api.databaseDownloadURL(name, db.path)
  }

  return (
    <>
      {error && <p className="error">{error}</p>}
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}

      <Section
        title="Databases"
        description="SQLite files in the app's DATA_DIR. A download is a consistent snapshot, safe to take while the app is running."
      >
        {data && data.databases.length === 0 && (
          <p className="muted">No SQLite files yet. They show up once the app creates one, or import one below.</p>
        )}
        <div className="kv">
          {data?.databases.map((db) => (
            <div key={db.path} className="kv-row">
              <code className="kv-key">{db.path}</code>
              <span className="muted">
                {formatBytes(db.size)} · changed {timeAgo(db.modifiedAt)}
              </span>
              <span />
              <div className="row-actions">
                <button className="ghost small" onClick={() => download(db)}>
                  Download
                </button>
                <button
                  className="ghost small"
                  onClick={() => {
                    setPath(db.path)
                    importRef.current?.scrollIntoView({ behavior: 'smooth' })
                  }}
                >
                  Replace…
                </button>
              </div>
            </div>
          ))}
        </div>
      </Section>

      {data && data.databases.length > 0 && (
        <>
          {data.databases.length > 1 && (
            <label className="field" style={{ marginBottom: 14 }}>
              <span>Database</span>
              <select value={selected} onChange={(e) => setSelected(e.target.value)}>
                {data.databases.map((db) => (
                  <option key={db.path}>{db.path}</option>
                ))}
              </select>
            </label>
          )}
          <RemoteBackups
            name={name}
            path={selected}
            onRestored={() => {
              reload()
              onChange()
            }}
          />
        </>
      )}

      <div ref={importRef}>
        <Section
          title="Import"
          description="Replaces the file at this path, or creates it. A running app stops for a few seconds while the file is swapped, then starts again. The current file is kept as a backup, and if the app fails its health check with the new one, the old one is put back."
        >
          <div className="stack">
            <label className="field">
              <span>Path in DATA_DIR</span>
              <input value={target} onChange={(e) => setPath(e.target.value)} spellCheck={false} disabled={busy} />
            </label>
            <label className="field">
              <span>SQLite file</span>
              <input
                ref={fileRef}
                type="file"
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
                disabled={busy}
              />
              <small className="muted">
                To copy a live database without stopping it: <code>sqlite3 app.db ".backup copy.db"</code>
              </small>
            </label>
            <div className="inline-form">
              <button className="primary" disabled={!file || busy || !target.trim()} onClick={runImport}>
                {busy ? (progress! < 1 ? `Uploading ${Math.round(progress! * 100)}%` : 'Importing…') : 'Import'}
              </button>
              {file && !busy && <span className="muted">{formatBytes(file.size)}</span>}
            </div>
            {busy && (
              <div className="progress">
                <div style={{ width: `${Math.round(progress! * 100)}%` }} />
              </div>
            )}
          </div>
        </Section>
      </div>

      {data && data.backups.length > 0 && (
        <Section title="Previous versions" description="Files replaced by imports, newest first. The last 10 are kept.">
          <div className="kv">
            {data.backups.map((db) => (
              <div key={db.path} className="kv-row">
                <code className="kv-key">{db.path.replace('.sideyard/backups/', '')}</code>
                <span className="muted">{formatBytes(db.size)}</span>
                <span />
                <div className="row-actions">
                  <button className="ghost small" onClick={() => download(db)}>
                    Download
                  </button>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
