import { Fragment, useRef, useState, type DragEvent, type FormEvent } from 'react'
import { File as FileIcon, Folder, FolderPlus, Search, Upload } from 'lucide-react'
import { api, errorMessage, type S3Object, type StorageInfo, type StorageInput } from './api'
import { formatBytes, go, timeAgo, usePoll } from './lib'
import { ConfirmButton, Dialog, Section } from './ui'
import { BucketFields } from './BucketFields'

/** The Storage page; prefix is the folder being browsed, from the URL. */
export function StorageTab({ name, prefix }: { name: string; prefix: string }) {
  const { data: info, error, reload } = usePoll(() => api.storage(name), 0, name)
  const [editing, setEditing] = useState(false)

  if (error) return <p className="error">{error}</p>
  if (!info) return null
  if (!info.connected || editing) {
    return (
      <ConnectForm
        name={name}
        info={info}
        onCancel={info.connected ? () => setEditing(false) : undefined}
        onDone={() => {
          setEditing(false)
          reload()
        }}
      />
    )
  }
  return (
    <>
      <BucketCard name={name} info={info} onEdit={() => setEditing(true)} onDisconnected={reload} />
      {/* A new folder starts with a fresh list, filter and selection. */}
      <Browser key={prefix} name={name} bucket={info.bucket} prefix={prefix} />
    </>
  )
}

function ConnectForm({
  name,
  info,
  onDone,
  onCancel,
}: {
  name: string
  info: StorageInfo
  onDone: () => void
  onCancel?: () => void
}) {
  const [form, setForm] = useState<StorageInput>({
    endpoint: info.endpoint,
    region: info.region,
    bucket: info.bucket,
    accessKey: info.accessKey,
    secretKey: '',
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await api.connectStorage(name, form)
      if (res.error) setError(res.error)
      onDone()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section
      title={info.connected ? 'Edit bucket' : 'Connect a bucket'}
      description={
        <>
          A bucket from any S3-compatible provider, with an access key that can reach <strong>only that bucket</strong>. The
          app gets the details as variables.
        </>
      }
    >
      <form onSubmit={submit} className="stack">
        <BucketFields
          value={form}
          onChange={(v) => setForm({ ...v, secretKey: v.secretKey ?? '' })}
          secretSaved={info.connected}
          required
        />
        {error && <p className="error">{error}</p>}
        <div className="inline-form">
          <button className="primary" disabled={busy}>
            {busy ? 'Checking the bucket…' : info.connected ? 'Save' : 'Connect'}
          </button>
          {onCancel && (
            <button type="button" className="ghost" onClick={onCancel}>
              Cancel
            </button>
          )}
        </div>
      </form>
    </Section>
  )
}

function BucketCard({
  name,
  info,
  onEdit,
  onDisconnected,
}: {
  name: string
  info: StorageInfo
  onEdit: () => void
  onDisconnected: () => void
}) {
  const [usage, setUsage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function count() {
    setUsage('Counting…')
    try {
      const u = await api.storageUsage(name)
      setUsage(`${u.truncated ? 'At least ' : ''}${formatBytes(u.bytes)} in ${u.objects.toLocaleString()} files`)
    } catch (e) {
      setUsage(errorMessage(e))
    }
  }

  async function disconnect() {
    const res = await api.disconnectStorage(name).catch((e) => ({ restarted: false, error: errorMessage(e) }))
    if (res.error) setError(res.error)
    onDisconnected()
  }

  return (
    <Section
      title={info.bucket}
      description={
        <>
          {info.endpoint || 'AWS S3'}
          {info.region && ` · ${info.region}`} · access key <code>{info.accessKey}</code>
        </>
      }
    >
      <div className="inline-form" style={{ marginTop: 0 }}>
        <button className="ghost small" onClick={count}>
          Count usage
        </button>
        {usage && <span className="muted">{usage}</span>}
        <span className="grow" />
        <button className="ghost small" onClick={onEdit}>
          Edit
        </button>
        <ConfirmButton label="Disconnect" confirmLabel="Disconnect bucket" onConfirm={disconnect} />
      </div>
      {error && <p className="error">{error}</p>}
      <details className="help">
        <summary>How the app reaches this bucket</summary>
        <p className="muted">
          The app sees <code>AWS_ENDPOINT_URL_S3</code>, <code>AWS_REGION</code>, <code>AWS_ACCESS_KEY_ID</code>,{' '}
          <code>AWS_SECRET_ACCESS_KEY</code> and <code>S3_BUCKET</code>. The AWS SDKs read them without configuration.
          Disconnecting removes them; the files stay in the bucket.
        </p>
      </details>
    </Section>
  )
}

/** The storage page's URL for a folder. */
function folderPath(name: string, prefix: string) {
  return `/p/${encodeURIComponent(name)}/storage${prefix ? `/${encodeURIComponent(prefix)}` : ''}`
}

function Browser({ name, bucket, prefix }: { name: string; bucket: string; prefix: string }) {
  // Pages loaded with "Load more", and the token for the page after them.
  const [extra, setExtra] = useState<S3Object[]>([])
  const [extraNext, setExtraNext] = useState('')
  const { data, error, reload } = usePoll(() => api.objects(name, prefix), 0, `${name}:${prefix}`)
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [details, setDetails] = useState<S3Object | null>(null)
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [dropping, setDropping] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  // S3 lists keys alphabetically; show folders first, like a file browser.
  const listed = [...(data?.objects ?? []), ...extra]
  const objects = [...listed.filter((o) => o.isPrefix), ...listed.filter((o) => !o.isPrefix)]
  const next = extra.length > 0 ? extraNext : (data?.next ?? '')
  const more = next !== ''
  const parts = prefix.split('/').filter(Boolean)
  const label = (o: S3Object) => o.key.slice(prefix.length)
  const q = filter.trim().toLowerCase()
  const shown = q ? objects.filter((o) => label(o).toLowerCase().includes(q)) : objects
  const shownFiles = shown.filter((o) => !o.isPrefix)
  const allSelected = shownFiles.length > 0 && shownFiles.every((o) => selected.has(o.key))

  function open(p: string) {
    go(folderPath(name, p))
  }

  async function loadMore() {
    const res = await api.objects(name, prefix, next)
    setExtraNext(res.next)
    setExtra([...extra, ...res.objects])
  }

  async function refresh() {
    setExtra([])
    setSelected(new Set())
    await reload()
  }

  async function upload(files: FileList | null) {
    if (!files?.length) return
    setStatus(null)
    try {
      for (const [i, f] of Array.from(files).entries()) {
        setProgress(0)
        await api.uploadObject(name, prefix, f, (x) => setProgress((i + x) / files.length))
      }
      setStatus({ ok: true, message: `Uploaded ${files.length} file${files.length === 1 ? '' : 's'}.` })
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    } finally {
      setProgress(null)
      if (fileRef.current) fileRef.current.value = ''
      refresh()
    }
  }

  async function remove(keys: string[]) {
    setStatus(null)
    setBusy(true)
    let done = 0
    try {
      for (const key of keys) {
        await api.deleteObject(name, key)
        done++
      }
      setStatus({ ok: true, message: `Deleted ${done} file${done === 1 ? '' : 's'}.` })
    } catch (e) {
      setStatus({ ok: false, message: `${done ? `Deleted ${done}, then: ` : ''}${errorMessage(e)}` })
    } finally {
      setBusy(false)
      setDetails(null)
      refresh()
    }
  }

  function toggle(key: string) {
    const next = new Set(selected)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setSelected(next)
  }

  function createFolder(e: FormEvent) {
    e.preventDefault()
    const folder = (newFolder ?? '').trim().replace(/^\/+|\/+$/g, '')
    if (!folder) return
    open(`${prefix}${folder}/`)
  }

  function onDrop(e: DragEvent) {
    e.preventDefault()
    setDropping(false)
    if (progress === null) upload(e.dataTransfer.files)
  }

  return (
    <section
      className={`card section browser ${dropping ? 'dropping' : ''}`}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes('Files')) return
        e.preventDefault()
        setDropping(true)
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropping(false)
      }}
      onDrop={onDrop}
    >
      <nav className="crumbs" aria-label="Folder">
        {parts.length ? <a href={`#${folderPath(name, '')}`}>{bucket}</a> : <strong>{bucket}</strong>}
        {parts.map((part, i) => {
          const p = parts.slice(0, i + 1).join('/') + '/'
          return (
            <Fragment key={p}>
              <span className="muted">/</span>
              {i === parts.length - 1 ? <strong>{part}</strong> : <a href={`#${folderPath(name, p)}`}>{part}</a>}
            </Fragment>
          )
        })}
      </nav>

      <div className="browser-bar">
        <div className="search">
          <Search size={14} aria-hidden />
          <input
            type="search"
            aria-label="Filter what's listed here"
            placeholder="Filter what's listed here"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            spellCheck={false}
          />
        </div>
        <span className="grow" />
        {newFolder === null ? (
          <button className="ghost small" onClick={() => setNewFolder('')}>
            <FolderPlus size={14} aria-hidden /> New folder
          </button>
        ) : (
          <form className="inline-form" style={{ marginTop: 0 }} onSubmit={createFolder}>
            <input
              autoFocus
              placeholder="Folder name"
              value={newFolder}
              onChange={(e) => setNewFolder(e.target.value)}
              onKeyDown={(e) => e.key === 'Escape' && setNewFolder(null)}
              spellCheck={false}
            />
            <button className="ghost small" disabled={!newFolder.trim()}>
              Create
            </button>
            <button type="button" className="ghost small" onClick={() => setNewFolder(null)}>
              Cancel
            </button>
          </form>
        )}
        <input ref={fileRef} type="file" multiple hidden onChange={(e) => upload(e.target.files)} />
        <button
          className="primary small"
          disabled={progress !== null}
          onClick={() => fileRef.current?.click()}
          title={`Uploads to ${bucket}/${prefix}`}
        >
          <Upload size={14} aria-hidden /> {progress !== null ? `Uploading ${Math.round(progress * 100)}%` : 'Upload'}
        </button>
      </div>

      {selected.size > 0 && (
        <div className="selection-bar">
          <span>
            {selected.size} file{selected.size === 1 ? '' : 's'} selected
          </span>
          <button className="ghost small" onClick={() => setSelected(new Set())}>
            Clear
          </button>
          <span className="grow" />
          <ConfirmButton
            label={`Delete ${selected.size}`}
            confirmLabel={`Delete ${selected.size} file${selected.size === 1 ? '' : 's'}`}
            disabled={busy}
            onConfirm={() => remove([...selected])}
          />
        </div>
      )}
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
      {error && <p className="error">{error}</p>}

      {objects.length > 0 && (
        <div className="object-row object-head">
          <input
            type="checkbox"
            aria-label="Select all files listed"
            checked={allSelected}
            disabled={shownFiles.length === 0}
            onChange={() => {
              const next = new Set(selected)
              for (const o of shownFiles) {
                if (allSelected) next.delete(o.key)
                else next.add(o.key)
              }
              setSelected(next)
            }}
          />
          <span>Name</span>
          <span className="num">Size</span>
          <span className="num col-modified">Modified</span>
          <span />
        </div>
      )}
      <div className="objects">
        {shown.map((o) =>
          o.isPrefix ? (
            <div key={o.key} className="object-row">
              <span />
              <button className="object-name folder" onClick={() => open(o.key)}>
                <Folder size={14} aria-hidden /> <span className="ellipsis">{label(o)}</span>
              </button>
              <span />
              <span className="col-modified" />
              <span />
            </div>
          ) : (
            <div key={o.key} className={`object-row ${selected.has(o.key) ? 'selected' : ''}`}>
              <input
                type="checkbox"
                aria-label={`Select ${label(o)}`}
                checked={selected.has(o.key)}
                onChange={() => toggle(o.key)}
              />
              <button className="object-name" onClick={() => setDetails(o)} title={o.key}>
                <FileIcon size={14} aria-hidden /> <span className="ellipsis">{label(o)}</span>
              </button>
              <span className="muted num">{formatBytes(o.size)}</span>
              <span className="muted num col-modified">{o.modifiedAt ? timeAgo(o.modifiedAt) : ''}</span>
              <div className="row-actions">
                <a className="ghost small button-link" href={api.objectURL(name, o.key)}>
                  Download
                </a>
              </div>
            </div>
          ),
        )}
      </div>

      {data && objects.length === 0 && (
        <p className="muted empty">
          {prefix
            ? 'This folder is empty. Upload files, or drop them here, to fill it. S3 keeps a folder only while it holds files.'
            : 'The bucket is empty. Upload files, or drop them here.'}
        </p>
      )}
      {objects.length > 0 && shown.length === 0 && <p className="muted empty">Nothing listed here matches “{filter}”.</p>}
      <div className="browser-foot muted">
        {objects.length > 0 && (
          <span>
            {q ? `${shown.length} of ${objects.length}` : objects.length} item{objects.length === 1 ? '' : 's'}
            {more && ' loaded'}
          </span>
        )}
        {more && (
          <button className="link-button" onClick={loadMore}>
            Load more{q && ' to filter them too'}
          </button>
        )}
        <span className="grow" />
        {objects.length > 0 && <span>Drop files anywhere here to upload them to this folder.</span>}
      </div>

      {details && (
        <ObjectDetails
          name={name}
          object={details}
          busy={busy}
          onDelete={() => remove([details.key])}
          onClose={() => setDetails(null)}
        />
      )}
    </section>
  )
}

function ObjectDetails({
  name,
  object,
  busy,
  onDelete,
  onClose,
}: {
  name: string
  object: S3Object
  busy: boolean
  onDelete: () => void
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const url = api.objectURL(name, object.key)
  const view = api.objectURL(name, object.key, true)
  const fileName = object.key.split('/').pop()

  async function copy() {
    try {
      await navigator.clipboard.writeText(object.key)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }

  return (
    <Dialog
      onClose={onClose}
      footer={
        <>
          <ConfirmButton label="Delete" confirmLabel="Delete file" disabled={busy} onConfirm={onDelete} />
          <span className="grow" />
          <button onClick={() => window.open(view, '_blank', 'noopener')}>Open</button>
          <button className="primary" onClick={() => (window.location.href = url)}>
            Download
          </button>
        </>
      }
    >
      <h3 className="ellipsis" title={fileName}>
        {fileName}
      </h3>
      <div className="kv object-details">
        <div className="kv-row">
          <span className="muted">Path</span>
          <span className="inline-form" style={{ marginTop: 0 }}>
            <code className="grow" style={{ overflowWrap: 'anywhere' }}>
              {object.key}
            </code>
            <button className="ghost small" onClick={copy}>
              {copied ? 'Copied' : 'Copy'}
            </button>
          </span>
        </div>
        <div className="kv-row">
          <span className="muted">Size</span>
          <span>
            {formatBytes(object.size)} <span className="muted">({object.size.toLocaleString()} bytes)</span>
          </span>
        </div>
        {object.modifiedAt && (
          <div className="kv-row">
            <span className="muted">Modified</span>
            <span>
              {new Date(object.modifiedAt).toLocaleString()} <span className="muted">({timeAgo(object.modifiedAt)})</span>
            </span>
          </div>
        )}
      </div>
      <p className="muted" style={{ marginTop: 12 }}>
        Open and Download use a link that works for five minutes.
      </p>
    </Dialog>
  )
}
