import { useState } from 'react'
import { api, errorMessage } from './api'
import { parseEnv, timeAgo, usePoll } from './lib'
import { CopyButton, Section } from './ui'

export function VariablesTab({ name }: { name: string }) {
  const { data, error, reload } = usePoll(() => api.variables(name), 0, name)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)

  async function save(set: Record<string, string>, del: string[] = []) {
    setBusy(true)
    setStatus(null)
    try {
      const res = await api.updateVariables(name, set, del)
      if (res.error) setStatus({ ok: false, message: res.error })
      else setStatus({ ok: true, message: res.restarted ? 'Saved. The app restarted with the new values.' : 'Saved.' })
      setText('')
      setRevealed({})
      setConfirmDelete(null)
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    } finally {
      setBusy(false)
      reload()
    }
  }

  async function reveal(key: string) {
    if (key in revealed) {
      const { [key]: _, ...rest } = revealed
      setRevealed(rest)
      return
    }
    const { value } = await api.revealVariable(name, key)
    setRevealed({ ...revealed, [key]: value })
  }

  const parsed = parseEnv(text)
  const count = Object.keys(parsed).length
  return (
    <>
      {error && <p className="error">{error}</p>}
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}

      <Section title="Your variables" description="Stored encrypted and passed to the app. Saving restarts it without downtime.">
        {data && data.variables.length > 0 && (
          <div className="kv">
            {data.variables.map((v) => (
              <div key={v.key} className="kv-row">
                <code className="kv-key">{v.key}</code>
                <code className="kv-value ellipsis">{v.key in revealed ? revealed[v.key] : '••••••••••••'}</code>
                <span className="muted small-text">{timeAgo(v.updatedAt)}</span>
                <div className="row-actions">
                  {v.key in revealed && <CopyButton text={revealed[v.key]} />}
                  <button className="ghost small" onClick={() => reveal(v.key)}>
                    {v.key in revealed ? 'Hide' : 'Reveal'}
                  </button>
                  {confirmDelete === v.key ? (
                    <button className="danger small" disabled={busy} onClick={() => save({}, [v.key])}>
                      Confirm delete
                    </button>
                  ) : (
                    <button className="ghost small" onClick={() => setConfirmDelete(v.key)}>
                      Delete
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
        <textarea
          rows={4}
          spellCheck={false}
          aria-label="Variables to add or update, as KEY=value lines"
          placeholder={'Add or update: paste KEY=value lines, as in a .env file\nSTRIPE_SECRET_KEY=sk_live_...'}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <button className="primary" disabled={busy || count === 0} onClick={() => save(parsed)}>
          {busy ? 'Saving…' : count > 0 ? `Save ${count} variable${count === 1 ? '' : 's'}` : 'Save'}
        </button>
      </Section>

      <Section title="Built in" description="Set by Sideyard for every app; they can't be overridden.">
        <div className="kv">
          {data?.builtins.map((b) => (
            <div key={b.key} className="kv-row builtin">
              <code className="kv-key">{b.key}</code>
              <span className="muted">{b.description}</span>
            </div>
          ))}
        </div>
      </Section>
    </>
  )
}
