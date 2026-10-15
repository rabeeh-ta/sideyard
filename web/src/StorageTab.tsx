import { useRef, useState, type FormEvent } from 'react'
import { api, errorMessage, type S3Object, type StorageInfo, type StorageInput } from './api'
import { formatBytes, timeAgo, usePoll } from './lib'
import { ConfirmButton, Section } from './ui'

export function StorageTab({ name }: { name: string }) {
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
  return <Browser name={name} info={info} onEdit={() => setEditing(true)} onDisconnected={reload} />
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
  const set = (k: keyof StorageInput) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [k]: e.target.value })

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
          Any S3-compatible bucket (R2, B2, AWS S3, MinIO), with an access key that can reach <strong>only that bucket</strong>.
          The app gets the details as variables.
        </>
      }
    >
      <form onSubmit={submit} className="stack">
        <div className="grid2">
          <label className="field span2">
            <span>Endpoint</span>
            <input
              value={form.endpoint}
              onChange={set('endpoint')}
              placeholder="https://<account-id>.r2.cloudflarestorage.com"
              spellCheck={false}
            />
            <small className="muted">
              R2: <code>https://&lt;account-id&gt;.r2.cloudflarestorage.com</code> · B2:{' '}
              <code>https://s3.&lt;region&gt;.backblazeb2.com</code> · AWS: leave empty and set the region.
            </small>
          </label>
          <label className="field">
            <span>Bucket</span>
            <input value={form.bucket} onChange={set('bucket')} required spellCheck={false} />
          </label>
          <label className="field">
            <span>Region</span>
            <input value={form.region} onChange={set('region')} placeholder="auto" spellCheck={false} />
          </label>
          <label className="field">
            <span>Access key ID</span>
            <input value={form.accessKey} onChange={set('accessKey')} required spellCheck={false} autoComplete="off" />
          </label>
          <label className="field">
            <span>Secret access key</span>
            <input
              type="password"
              value={form.secretKey}
              onChange={set('secretKey')}
              required={!info.connected}
              placeholder={info.connected ? 'saved; leave empty to keep it' : ''}
              autoComplete="new-password"
            />
          </label>
        </div>
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

function Browser({
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
  const [prefix, setPrefix] = useState('')
  // Pages loaded with "Load more", and whether there are more after them.
  const [extra, setExtra] = useState<S3Object[]>([])
  const [extraMore, setExtraMore] = useState(false)
  const { data, error, reload } = usePoll(() => api.objects(name, prefix), 0, `${name}:${prefix}`)
  const [usage, setUsage] = useState<string | null>(null)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  // S3 lists keys alphabetically; show folders first, like a file browser.
  const listed = [...(data?.objects ?? []), ...extra]
  const objects = [...listed.filter((o) => o.isPrefix), ...listed.filter((o) => !o.isPrefix)]
  const more = extra.length > 0 ? extraMore : data?.more
  const parts = prefix.split('/').filter(Boolean)

  function open(p: string) {
    setExtra([])
    setPrefix(p)
  }

  async function loadMore() {
    const last = listed[listed.length - 1]
    const res = await api.objects(name, prefix, last.key)
    setExtraMore(res.more)
    setExtra([...extra, ...res.objects])
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
      setExtra([])
      reload()
    }
  }

  async function remove(key: string) {
    try {
      await api.deleteObject(name, key)
      setExtra(extra.filter((o) => o.key !== key))
      reload()
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    }
  }

  async function disconnect() {
    const res = await api.disconnectStorage(name).catch((e) => ({ restarted: false, error: errorMessage(e) }))
    if (res.error) setStatus({ ok: false, message: res.error })
    onDisconnected()
  }

  return (
    <>
      <Section
        title={info.bucket}
        description={
          <>
            {info.endpoint || 'AWS S3'}
            {info.region && ` · ${info.region}`} · access key <code>{info.accessKey}</code>
          </>
        }
      >
        <div className="inline-form">
          <button className="ghost small" onClick={onEdit}>
            Edit
          </button>
          <button
            className="ghost small"
            onClick={async () => {
              setUsage('Counting…')
              try {
                const u = await api.storageUsage(name)
                setUsage(`${u.truncated ? 'At least ' : ''}${formatBytes(u.bytes)} in ${u.objects.toLocaleString()} objects`)
              } catch (e) {
                setUsage(errorMessage(e))
              }
            }}
          >
            Count usage
          </button>
          {usage && <span className="muted">{usage}</span>}
          <span className="grow" />
          <ConfirmButton label="Disconnect" confirmLabel="Disconnect bucket" onConfirm={disconnect} />
        </div>
        <p className="muted help">
          The app sees <code>AWS_ENDPOINT_URL_S3</code>, <code>AWS_REGION</code>, <code>AWS_ACCESS_KEY_ID</code>,{' '}
          <code>AWS_SECRET_ACCESS_KEY</code> and <code>S3_BUCKET</code>. The AWS SDKs read them without configuration.
          Disconnecting removes them; the files stay in the bucket.
        </p>
      </Section>

      <section className="card section">
        <div className="inline-form" style={{ marginTop: 0 }}>
          <div className="crumbs grow">
            <button className="ghost small" onClick={() => open('')}>
              {info.bucket}
            </button>
            {parts.map((part, i) => (
              <span key={i}>
                /{' '}
                <button className="ghost small" onClick={() => open(parts.slice(0, i + 1).join('/') + '/')}>
                  {part}
                </button>
              </span>
            ))}
          </div>
          <input ref={fileRef} type="file" multiple hidden onChange={(e) => upload(e.target.files)} />
          <button className="primary small" disabled={progress !== null} onClick={() => fileRef.current?.click()}>
            {progress !== null ? `Uploading ${Math.round(progress * 100)}%` : 'Upload files'}
          </button>
        </div>
        {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
        {error && <p className="error">{error}</p>}
        {data && objects.length === 0 && <p className="muted">{prefix ? 'This folder is empty.' : 'The bucket is empty.'}</p>}
        <div className="kv">
          {objects.map((o) =>
            o.isPrefix ? (
              <div key={o.key} className="object-row">
                <button className="folder" onClick={() => open(o.key)}>
                  {o.key.slice(prefix.length)}
                </button>
                <span />
                <span />
                <span />
              </div>
            ) : (
              <div key={o.key} className="object-row">
                <span className="object-name ellipsis" title={o.key}>
                  {o.key.slice(prefix.length)}
                </span>
                <span className="muted num">{formatBytes(o.size)}</span>
                <span className="muted num">{timeAgo(o.modifiedAt)}</span>
                <div className="row-actions">
                  <a className="ghost small button-link" href={api.objectURL(name, o.key)}>
                    Download
                  </a>
                  <ConfirmButton label="Delete" onConfirm={() => remove(o.key)} />
                </div>
              </div>
            ),
          )}
        </div>
        {more && (
          <button className="link-button" onClick={loadMore}>
            Load more
          </button>
        )}
      </section>
    </>
  )
}
