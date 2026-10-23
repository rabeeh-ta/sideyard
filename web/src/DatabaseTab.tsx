import { useRef, useState } from 'react'
import { api, errorMessage, type Database } from './api'
import { formatBytes, timeAgo, usePoll } from './lib'
import { Dialog, Section } from './ui'
import { RemoteBackups } from './DatabaseTools'

export function DatabaseTab({ name, onChange }: { name: string; onChange: () => void }) {
  const { data, error, reload } = usePoll(() => api.databases(name), 0, name)
  const [path, setPath] = useState<string | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  // Replace… picks a file for one database, then asks before uploading it.
  const replaceInput = useRef<HTMLInputElement>(null)
  const [replaceTarget, setReplaceTarget] = useState('')
  const [replacing, setReplacing] = useState<{ path: string; file: File } | null>(null)

  // A new project's first database is usually app.db; otherwise the path is
  // for a file that isn't there yet.
  const target = path ?? (data?.databases.length ? '' : 'app.db')
  const exists = data?.databases.some((db) => db.path === target.trim()) ?? false
  const [chosen, setSelected] = useState<string | null>(null)
  const selected = chosen ?? data?.databases[0]?.path ?? ''
  const busy = progress !== null

  async function upload(to: string, from: File): Promise<boolean> {
    setStatus(null)
    setProgress(0)
    try {
      const res = await api.importDatabase(name, to, from, setProgress)
      setStatus({
        ok: true,
        message: res.backup
          ? `Imported ${res.path}. The previous version is kept as ${res.backup}.`
          : `Imported ${res.path}.`,
      })
      return true
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
      return false
    } finally {
      setProgress(null)
      reload()
      onChange()
    }
  }

  async function runImport() {
    if (file && (await upload(target.trim(), file))) {
      setFile(null)
      setPath(null)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  async function runReplace() {
    if (!replacing) return
    await upload(replacing.path, replacing.file)
    setReplacing(null)
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
                  disabled={busy}
                  onClick={() => {
                    setReplaceTarget(db.path)
                    replaceInput.current?.click()
                  }}
                >
                  Replace…
                </button>
              </div>
            </div>
          ))}
        </div>
        <input
          ref={replaceInput}
          type="file"
          hidden
          onChange={(e) => {
            const chosen = e.target.files?.[0]
            if (chosen) setReplacing({ path: replaceTarget, file: chosen })
            e.target.value = ''
          }}
        />
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

      <Section
        title="Add a database"
        description={
          data?.databases.length
            ? 'Uploads a SQLite file to a new path in DATA_DIR. To replace one of the databases above, use its Replace… button.'
            : 'Uploads a SQLite file to the path the app expects, for example the database of an app you are moving here.'
        }
      >
        <div className="stack">
          <label className="field">
            <span>Path in DATA_DIR</span>
            <input
              value={target}
              onChange={(e) => setPath(e.target.value)}
              placeholder="other.db"
              spellCheck={false}
              disabled={busy}
            />
            {exists && (
              <small className="muted">
                {target.trim()} already exists: this replaces it, and the current file is kept as a previous version.
              </small>
            )}
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
              {busy && !replacing ? uploadLabel(progress!) : exists ? 'Replace' : 'Import'}
            </button>
            {file && !busy && <span className="muted">{formatBytes(file.size)}</span>}
          </div>
          {busy && !replacing && <ProgressBar fraction={progress!} />}
        </div>
      </Section>

      {replacing && (
        <Dialog
          onClose={() => !busy && setReplacing(null)}
          footer={
            <>
              <button disabled={busy} onClick={() => setReplacing(null)}>
                Cancel
              </button>
              <button className="primary" disabled={busy} onClick={runReplace}>
                {busy ? uploadLabel(progress!) : `Replace ${replacing.path}`}
              </button>
            </>
          }
        >
          <h3>Replace {replacing.path}?</h3>
          <p className="muted">
            With <code>{replacing.file.name}</code> ({formatBytes(replacing.file.size)}). The app pauses for a few seconds. The
            current file is kept as a previous version, and put back if the app fails its health check.
          </p>
          {busy && <ProgressBar fraction={progress!} />}
        </Dialog>
      )}

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

function uploadLabel(fraction: number) {
  return fraction < 1 ? `Uploading ${Math.round(fraction * 100)}%` : 'Importing…'
}

function ProgressBar({ fraction }: { fraction: number }) {
  return (
    <div className="progress">
      <div style={{ width: `${Math.round(fraction * 100)}%` }} />
    </div>
  )
}
